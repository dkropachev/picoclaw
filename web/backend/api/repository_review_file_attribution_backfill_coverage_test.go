package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryReviewFileAttributionLoaderFunc func(
	context.Context,
	string,
) (*workflows.Run, error)

func (load repositoryReviewFileAttributionLoaderFunc) GetRun(
	ctx context.Context,
	runID string,
) (*workflows.Run, error) {
	return load(ctx, runID)
}

func TestPrepareRepositoryReviewFileAttributionBackfillBoundaries(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	repositoryReviewPrepareAttributionFixtureRuns(t, &fixture)
	loader := repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}}

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := prepareRepositoryReviewFileAttributionBackfill(
			ctx, fixture.automation, fixture.state, loader,
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
	tests := []struct {
		name   string
		mutate func(*repoaudit.RepositoryReviewAutomation, *repoaudit.RepositoryState)
		store  repositoryReviewWorkflowRunLoader
		want   error
	}{
		{name: "nil store", store: nil, want: repoaudit.ErrInvalidAutomation},
		{
			name: "blank automation", store: loader,
			mutate: func(a *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState) {
				a.ID = ""
			},
			want: repoaudit.ErrInvalidAutomation,
		},
		{
			name: "blank state", store: loader,
			mutate: func(_ *repoaudit.RepositoryReviewAutomation, s *repoaudit.RepositoryState) {
				s.Repository = ""
			},
			want: repoaudit.ErrInvalidAutomation,
		},
		{
			name:  "active",
			store: loader,
			mutate: func(a *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState) {
				a.ActiveRunID = "wr_active"
			},
			want: repoaudit.ErrInvalidAutomation,
		},
		{
			name:  "running",
			store: loader,
			mutate: func(a *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState) {
				a.Status = repoaudit.RepositoryReviewAutomationRunning
			},
			want: repoaudit.ErrInvalidAutomation,
		},
		{
			name:  "stopping",
			store: loader,
			mutate: func(a *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState) {
				a.Status = repoaudit.RepositoryReviewAutomationStopping
			},
			want: repoaudit.ErrInvalidAutomation,
		},
		{
			name:   "no runs",
			store:  loader,
			mutate: func(a *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState) { a.RunIDs = nil },
			want:   repoaudit.ErrInvalidAutomation,
		},
		{
			name:  "too many runs",
			store: loader,
			mutate: func(a *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState) {
				a.RunIDs = make([]string, repositoryReviewLegacyBackfillMaxRuns)
			},
			want: repoaudit.ErrInvalidAutomation,
		},
		{
			name:  "identity mismatch",
			store: loader,
			mutate: func(_ *repoaudit.RepositoryReviewAutomation, s *repoaudit.RepositoryState) {
				s.Repository = "other/repository"
			},
			want: repoaudit.ErrInvalidAutomation,
		},
		{
			name:   "blank run",
			store:  loader,
			mutate: func(a *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState) { a.RunIDs[0] = " " },
			want:   repoaudit.ErrInvalidAutomation,
		},
		{
			name:  "duplicate run",
			store: loader,
			mutate: func(a *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState) {
				a.RunIDs = append(a.RunIDs, a.RunIDs[0])
			},
			want: repoaudit.ErrInvalidAutomation,
		},
		{
			name:  "duplicate ledger run",
			store: loader,
			mutate: func(_ *repoaudit.RepositoryReviewAutomation, s *repoaudit.RepositoryState) {
				s.Runs = append(s.Runs, s.Runs[0])
			},
			want: repoaudit.ErrInvalidPlan,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			automation := fixture.automation
			automation.RunIDs = append([]string(nil), automation.RunIDs...)
			state := fixture.state
			state.Runs = append([]repoaudit.ReviewRun(nil), state.Runs...)
			if test.mutate != nil {
				test.mutate(&automation, &state)
			}
			_, err := prepareRepositoryReviewFileAttributionBackfill(
				t.Context(), automation, state, test.store,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
	t.Run("unrelated ledger run is ignored", func(t *testing.T) {
		state := fixture.state
		state.Runs = append(append([]repoaudit.ReviewRun(nil), state.Runs...), repoaudit.ReviewRun{ID: "wr_unrelated"})
		if _, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(), fixture.automation, state, loader,
		); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("canceled between runs", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		automation := fixture.automation
		automation.RunIDs = append(append([]string(nil), automation.RunIDs...), "wr_never_loaded")
		_, err := prepareRepositoryReviewFileAttributionBackfill(
			ctx, automation, fixture.state,
			repositoryReviewFileAttributionLoaderFunc(func(_ context.Context, runID string) (*workflows.Run, error) {
				if runID == fixture.automation.RunIDs[0] {
					cancel()
					return fixture.runs[runID], nil
				}
				t.Fatal("canceled preparation loaded a second run")
				return nil, nil
			}),
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("invalid recovered campaign credit preview", func(t *testing.T) {
		campaignID := repoaudit.NewRepositoryReviewCampaignID()
		automation := fixture.automation
		automation.CampaignID = campaignID
		state := fixture.state
		state.CurrentCampaign = &repoaudit.RepositoryReviewCampaignCoverage{
			ID: campaignID, Exact: true, RecoveryDigest: "sha256:recovered",
			AssignmentCatalog: []repoaudit.RepositoryReviewAssignment{{}},
			Paths:             map[string]repoaudit.RepositoryReviewCampaignPathCoverage{},
		}
		_, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(), automation, state, loader,
		)
		if !errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestPrepareRepositoryReviewFileAttributionBackfillRunFailures(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	repositoryReviewPrepareAttributionFixtureRuns(t, &fixture)
	runID := fixture.automation.RunIDs[0]
	original := fixture.runs[runID]
	boom := errors.New("load failed")
	tests := []struct {
		name   string
		state  func(repoaudit.RepositoryState) repoaudit.RepositoryState
		loader repositoryReviewWorkflowRunLoader
		want   error
	}{
		{
			name: "load error",
			loader: repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return nil, boom },
			),
			want: boom,
		},
		{
			name: "nil run",
			loader: repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return nil, nil },
			),
			want: repoaudit.ErrInvalidPlan,
		},
		{
			name: "unmarshalable run",
			loader: repositoryReviewFileAttributionLoaderFunc(func(context.Context, string) (*workflows.Run, error) {
				return &workflows.Run{ID: runID, Inputs: map[string]any{"bad": make(chan int)}}, nil
			}),
			want: repoaudit.ErrInvalidPlan,
		},
		{
			name:  "disallowed non-ledger",
			state: func(state repoaudit.RepositoryState) repoaudit.RepositoryState { state.Runs = nil; return state },
			loader: repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return original, nil },
			),
			want: repoaudit.ErrInvalidPlan,
		},
		{
			name: "invalid retained evidence",
			loader: repositoryReviewFileAttributionLoaderFunc(func(context.Context, string) (*workflows.Run, error) {
				clone := *original
				clone.Status = workflows.RunStatusFailed
				return &clone, nil
			}),
			want: repoaudit.ErrInvalidPlan,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := fixture.state
			if test.state != nil {
				state = test.state(state)
			}
			_, err := prepareRepositoryReviewFileAttributionBackfill(
				t.Context(), fixture.automation, state, test.loader,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}

	t.Run("invalid root agent", func(t *testing.T) {
		clone := cloneRepositoryReviewAttributionRun(t, original)
		review := clone.Steps["find_bugs/review"]
		review.Outputs["agent_id"] = "secondary"
		clone.Steps["find_bugs/review"] = review
		_, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			fixture.state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return clone, nil },
			),
		)
		if !errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("invalid child index", func(t *testing.T) {
		clone := cloneRepositoryReviewAttributionRun(t, original)
		review := clone.Steps["find_bugs/review"]
		children := review.Outputs["managed_children"].([]any)
		children[0].(map[string]any)["index"] = 0
		review.Outputs["managed_children"] = children
		clone.Steps["find_bugs/review"] = review
		_, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			fixture.state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return clone, nil },
			),
		)
		if !errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("invalid usage", func(t *testing.T) {
		clone := cloneRepositoryReviewAttributionRun(t, original)
		review := clone.Steps["find_bugs/review"]
		children := review.Outputs["managed_children"].([]any)
		children[0].(map[string]any)["usage"] = "invalid"
		review.Outputs["managed_children"] = children
		clone.Steps["find_bugs/review"] = review
		_, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			fixture.state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return clone, nil },
			),
		)
		if !errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("raw child decode failure", func(t *testing.T) {
		clone := cloneRepositoryReviewAttributionRun(t, original)
		review := clone.Steps["find_bugs/review"]
		children := review.Outputs["managed_children"].([]any)
		children[0].(map[string]any)["coverage_test"] = &repositoryReviewFailSecondJSON{}
		review.Outputs["managed_children"] = children
		clone.Steps["find_bugs/review"] = review
		_, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			fixture.state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return clone, nil },
			),
		)
		if !errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("attribution constructor failure", func(t *testing.T) {
		automation := fixture.automation
		automation.ID = "invalid automation id"
		_, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			automation,
			fixture.state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return original, nil },
			),
		)
		if !errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("existing attribution conflict", func(t *testing.T) {
		initial, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			fixture.state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return original, nil },
			),
		)
		if err != nil || len(initial.attributions) != 1 {
			t.Fatalf("initial=%#v err=%v", initial, err)
		}
		state := fixture.state
		conflict := initial.attributions[0]
		conflict.Model = "conflicting-model"
		state.FileAttributions = []repoaudit.RepositoryReviewFileAttribution{conflict}
		_, err = prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return original, nil },
			),
		)
		if !errors.Is(err, repoaudit.ErrConflict) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("prepared envelope digest failure", func(t *testing.T) {
		initial, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			fixture.state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return original, nil },
			),
		)
		if err != nil || len(initial.attributions) != 1 {
			t.Fatalf("initial=%#v err=%v", initial, err)
		}
		live := initial.attributions[0]
		live.Source = repoaudit.RepositoryReviewFileAttributionSourceLiveCheckpoint
		live.Model = live.UsageModel
		live.ModelAlias = live.ReviewerIdentity
		live.Account = "resolved-account"
		live.UsageModel = ""
		live.CompletedAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
		state := fixture.state
		state.FileAttributions = []repoaudit.RepositoryReviewFileAttribution{live}
		_, err = prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return original, nil },
			),
		)
		if !errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("stale apply", func(t *testing.T) {
		prepared, err := prepareRepositoryReviewFileAttributionBackfill(
			t.Context(),
			fixture.automation,
			fixture.state,
			repositoryReviewFileAttributionLoaderFunc(
				func(context.Context, string) (*workflows.Run, error) { return original, nil },
			),
		)
		if err != nil || len(prepared.attributions) != 1 {
			t.Fatalf("prepared=%#v err=%v", prepared, err)
		}
		blocker := prepared.attributions[0]
		blocker.ID = ""
		blocker.ChildIndex++
		blocker, err = repoaudit.NewRepositoryReviewFileAttribution(blocker)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.MergeRepositoryReviewFileAttributions(
			t.Context(), repoaudit.MergeRepositoryReviewFileAttributionsRequest{
				Repository: fixture.state.Repository, ExpectedVersion: fixture.state.Version,
				Attributions: []repoaudit.RepositoryReviewFileAttribution{blocker},
			},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := applyPreparedRepositoryReviewFileAttributionBackfill(
			t.Context(), fixture.store, fixture.state, prepared,
		); !errors.Is(err, repoaudit.ErrConflict) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestRepositoryReviewFileAttributionHelperBoundaries(t *testing.T) {
	fileA := repoaudit.FileRef{Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1, Mode: "100644"}
	fileB := repoaudit.FileRef{Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2, Mode: "100644"}
	fileC := repoaudit.FileRef{Path: "c.go", BlobSHA: strings.Repeat("c", 40), SizeBytes: 3, Mode: "100644"}
	plan := repoaudit.Plan{PendingFiles: []repoaudit.FileRef{fileA, fileB, fileC}}
	evidence := workflows.RepositoryReviewManagedEvidence{
		CompletedFiles:   []repoaudit.FileRef{fileA},
		UnsupportedFiles: []repoaudit.UnsupportedFile{{FileRef: fileB, Reason: "terminal"}},
		Observations: []repoaudit.Observation{
			{Model: "model-a", ModelAlias: "review"},
			{Model: "model-b", ModelAlias: "review"},
			{Model: "model-c"},
		},
	}
	ledger := repoaudit.ReviewRun{
		ReviewedFiles: 1, UnsupportedCount: 1, UnreviewedFiles: 1,
		RemainingFiles: 1, UnreviewedPaths: []string{"c.go"},
		UnsupportedPaths: []string{"b.go"}, Models: []string{"review", "model-c"},
	}
	if !repositoryReviewFileAttributionEvidenceMatchesLedger(plan, evidence, ledger) {
		t.Fatal("valid evidence did not match")
	}
	badPath := ledger
	badPath.UnsupportedPaths = []string{"missing.go"}
	if repositoryReviewFileAttributionEvidenceMatchesLedger(plan, evidence, badPath) {
		t.Fatal("missing unsupported path matched")
	}
	badUnsupported := evidence
	badUnsupported.UnsupportedFiles = []repoaudit.UnsupportedFile{{FileRef: fileC, Reason: "terminal"}}
	if repositoryReviewFileAttributionEvidenceMatchesLedger(plan, badUnsupported, ledger) {
		t.Fatal("wrong unsupported evidence matched")
	}
	mutations := []func(*repoaudit.ReviewRun){
		func(run *repoaudit.ReviewRun) { run.UnsupportedCount++ },
		func(run *repoaudit.ReviewRun) { run.ReviewedFiles++ },
		func(run *repoaudit.ReviewRun) { run.SkippedFiles++ },
		func(run *repoaudit.ReviewRun) { run.UnreviewedFiles++ },
		func(run *repoaudit.ReviewRun) { run.RemainingFiles++ },
		func(run *repoaudit.ReviewRun) { run.UnreviewedPaths = nil },
		func(run *repoaudit.ReviewRun) { run.Models = nil },
		func(run *repoaudit.ReviewRun) { run.RejectedFindings = 1 },
	}
	for index, mutate := range mutations {
		candidate := ledger
		mutate(&candidate)
		if repositoryReviewFileAttributionEvidenceMatchesLedger(plan, evidence, candidate) {
			t.Fatalf("ledger mutation %d matched", index)
		}
	}

	base := repoaudit.RepositoryReviewFileAttribution{
		ID: "rfa_same", AutomationID: "rra_same", RunID: "run", CommitSHA: strings.Repeat("d", 40),
		InventoryHash: "inventory", ProfileHash: "profile", AssignmentID: "assignment",
		FocusID: repoaudit.RepositoryReviewFocusSecurityTrust, RootAgentID: "main",
		ReviewerIdentity: "review", Model: "provider/model", ModelAlias: "review", Account: "account",
		AcknowledgedFiles: []repoaudit.FileRef{fileA}, EvidenceDigest: "sha256:" + strings.Repeat("e", 64),
		Source:     repoaudit.RepositoryReviewFileAttributionSourceLegacyManagedChild,
		ChildIndex: 1, Required: true,
	}
	if got, err := repositoryReviewResolveExistingFileAttribution(base, base); err != nil || got.ID != base.ID {
		t.Fatalf("legacy replay=%#v err=%v", got, err)
	}
	otherID := base
	otherID.ID = "rfa_other"
	if _, err := repositoryReviewResolveExistingFileAttribution(otherID, base); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("different ID error=%v", err)
	}
	changedLegacy := base
	changedLegacy.Model = "other"
	if _, err := repositoryReviewResolveExistingFileAttribution(
		changedLegacy,
		base,
	); !errors.Is(
		err,
		repoaudit.ErrConflict,
	) {
		t.Fatalf("changed legacy error=%v", err)
	}
	unknown := base
	unknown.Source = "unknown"
	if _, err := repositoryReviewResolveExistingFileAttribution(unknown, base); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("unknown source error=%v", err)
	}
	live := base
	live.Source = repoaudit.RepositoryReviewFileAttributionSourceLiveCheckpoint
	live.Model = "provider/model"
	recovered := base
	recovered.UsageModel = "provider/model"
	if got, err := repositoryReviewResolveExistingFileAttribution(
		live,
		recovered,
	); err != nil ||
		got.Source != live.Source {
		t.Fatalf("live replay=%#v err=%v", got, err)
	}
	incompatible := live
	incompatible.RunID = "other"
	if _, err := repositoryReviewResolveExistingFileAttribution(
		incompatible,
		recovered,
	); !errors.Is(
		err,
		repoaudit.ErrConflict,
	) {
		t.Fatalf("incompatible live error=%v", err)
	}
	modelFallback := recovered
	modelFallback.UsageModel = ""
	modelFallback.ModelAlias = "review"
	if !repositoryReviewLiveAttributionCoversRecovery(live, modelFallback) {
		t.Fatal("legacy model fallback did not match")
	}
	wrongModel := recovered
	wrongModel.UsageModel = "provider/other"
	if repositoryReviewLiveAttributionCoversRecovery(live, wrongModel) {
		t.Fatal("wrong model matched")
	}
	accounted := recovered
	accounted.Account = "other-account"
	if repositoryReviewLiveAttributionCoversRecovery(live, accounted) {
		t.Fatal("wrong account matched")
	}
}

func TestRepositoryReviewLegacyChildParsingBoundaries(t *testing.T) {
	for _, child := range []map[string]any{{}, {"index": 0}, {"index": "one"}} {
		if _, ok := repositoryReviewLegacyChildIndex(child); ok {
			t.Fatalf("invalid child index accepted: %#v", child)
		}
	}
	if index, ok := repositoryReviewLegacyChildIndex(map[string]any{"index": 7}); !ok || index != 7 {
		t.Fatalf("index=%d ok=%v", index, ok)
	}
	if model, err := repositoryReviewLegacyChildUsageModel(map[string]any{}, "review"); err != nil || model != "" {
		t.Fatalf("missing usage=%q err=%v", model, err)
	}
	if model, err := repositoryReviewLegacyChildUsageModel(
		map[string]any{"usage": nil},
		"review",
	); err != nil ||
		model != "" {
		t.Fatalf("nil usage=%q err=%v", model, err)
	}
	invalid := []any{
		"bad",
		[]map[string]any{},
		[]map[string]any{{"model": "", "reviewer": "review"}},
		[]map[string]any{{"model": strings.Repeat("m", 257), "reviewer": "review"}},
		[]map[string]any{{"model": "model", "reviewer": "other"}},
	}
	for index, usage := range invalid {
		if _, err := repositoryReviewLegacyChildUsageModel(
			map[string]any{"usage": usage},
			"review",
		); !errors.Is(
			err,
			repoaudit.ErrInvalidPlan,
		) {
			t.Fatalf("invalid usage %d error=%v", index, err)
		}
	}
	multiple := map[string]any{"usage": []map[string]any{
		{"model": "model-a", "reviewer": "review"},
		{"model": "model-b", "reviewer": "review"},
	}}
	if model, err := repositoryReviewLegacyChildUsageModel(multiple, "review"); err != nil || model != "" {
		t.Fatalf("ambiguous models=%q err=%v", model, err)
	}
	defaultWithoutSelected := map[string]any{
		"model": "bad", "usage": []map[string]any{{"model": "model", "reviewer": "fallback"}},
	}
	if _, err := repositoryReviewLegacyChildUsageModel(
		defaultWithoutSelected,
		"default",
	); !errors.Is(
		err,
		repoaudit.ErrInvalidPlan,
	) {
		t.Fatalf("default without selected error=%v", err)
	}
}

func TestRepositoryReviewFileAttributionEnvelopeDigestBoundaries(t *testing.T) {
	envelope := repositoryReviewFileAttributionDigestEnvelope{
		Schema: "repository-review-file-attributions-v1",
	}
	digest, err := repositoryReviewFileAttributionEnvelopeDigest(envelope, 1<<20)
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	if _, err := repositoryReviewFileAttributionEnvelopeDigest(envelope, 1); !errors.Is(err, repoaudit.ErrInvalidPlan) {
		t.Fatalf("small envelope error=%v", err)
	}
	envelope.Attributions = []repoaudit.RepositoryReviewFileAttribution{{
		CompletedAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
	}}
	if _, err := repositoryReviewFileAttributionEnvelopeDigest(
		envelope,
		1<<20,
	); !errors.Is(
		err,
		repoaudit.ErrInvalidPlan,
	) {
		t.Fatalf("unmarshalable envelope error=%v", err)
	}
}

func cloneRepositoryReviewAttributionRun(t *testing.T, run *workflows.Run) *workflows.Run {
	t.Helper()
	loader := repositoryReviewBackfillLoader{
		runs: map[string]*workflows.Run{run.ID: run}, err: map[string]error{},
	}
	clone, err := loader.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return clone
}

type repositoryReviewFailSecondJSON struct{ calls int }

func (value *repositoryReviewFailSecondJSON) MarshalJSON() ([]byte, error) {
	value.calls++
	if value.calls > 1 {
		return nil, errors.New("second marshal failed")
	}
	return []byte(`"first marshal"`), nil
}
