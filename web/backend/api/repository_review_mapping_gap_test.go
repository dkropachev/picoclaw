package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryMappingControllerAndGitFailureBranches(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}

	poisonedWorkspace := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(poisonedWorkspace, "repository_reviews"), []byte("not a directory"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	poisonedController := newRepositoryReviewController(handler)
	poisonedController.leasedConfig = cfg
	poisonedController.leasedStore = repoaudit.NewStore(poisonedWorkspace)
	if err := poisonedController.processRepositoryFindingMappings(t.Context(), nil); err == nil {
		t.Fatal("poisoned mapping catalog was accepted")
	}
	if err := poisonedController.processRepositoryFindingValidations(t.Context(), nil); err == nil {
		t.Fatal("poisoned validation catalog was accepted")
	}

	fixture := newRepositoryReviewValidationGitFixture(t)
	store := repoaudit.NewStore(workspace)
	_ = seedRepositoryReviewAPIState(t, workspace)
	controller := newRepositoryReviewController(handler)
	controller.leasedConfig = cfg
	controller.leasedStore = store
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := controller.processRepositoryFindingMappings(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mapping error=%v", err)
	}

	unavailable := repoaudit.RepositoryReviewAutomation{
		ID: "rra_unavailable", Repository: "https://127.0.0.1:1/unavailable.git",
	}
	reachabilityState := repoaudit.RepositoryState{Findings: []repoaudit.Finding{{
		CommitSHA: strings.Repeat("a", 40), TargetIsDefault: true,
	}}}
	verify, regression, release := repositoryMappingDefaultVerifier(
		t.Context(), cfg, unavailable, reachabilityState,
	)
	defer release()
	if _, err := verify(t.Context(), reachabilityState.Findings[0]); err == nil {
		t.Fatal("unavailable default checkout did not report its acquisition error")
	}
	if _, err := regression(
		t.Context(), reachabilityState.Findings[0], repoaudit.RepositoryFinding{},
	); err == nil {
		t.Fatal("unavailable regression checkout did not report its acquisition error")
	}

	invalidRoot := filepath.Join(t.TempDir(), "rename-root-file")
	if err := os.WriteFile(invalidRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidCfg := *cfg
	invalidCfg.GitWorkspaces.RootDir = invalidRoot
	if equivalent := repositoryMappingRenameEquivalent(
		t.Context(), &invalidCfg, unavailable,
		repoaudit.RepositoryState{
			LastCommitSHA:      strings.Repeat("a", 40),
			RepositoryFindings: []repoaudit.RepositoryFinding{{FoundCommits: []string{strings.Repeat("b", 40)}}},
		},
	); equivalent("old.go", "new.go") {
		t.Fatal("invalid rename workspace invented equivalence")
	}
	if equivalent := repositoryMappingRenameEquivalent(
		t.Context(), cfg, unavailable,
		repoaudit.RepositoryState{
			LastCommitSHA:      strings.Repeat("a", 40),
			RepositoryFindings: []repoaudit.RepositoryFinding{{FoundCommits: []string{strings.Repeat("b", 40)}}},
		},
	); equivalent("old.go", "new.go") {
		t.Fatal("unavailable rename checkout invented equivalence")
	}
	if equivalent := repositoryMappingRenameEquivalent(
		t.Context(), cfg,
		repoaudit.RepositoryReviewAutomation{ID: "rra_bad_diff", Repository: fixture.directory},
		repoaudit.RepositoryState{
			LastCommitSHA:      fixture.rename,
			RepositoryFindings: []repoaudit.RepositoryFinding{{FoundCommits: []string{strings.Repeat("f", 40)}}},
		},
	); equivalent("old.go", "new.go") {
		t.Fatal("failed rename diff invented equivalence")
	}
	manyCommits := make([]string, 201)
	for index := range manyCommits {
		manyCommits[index] = fmt.Sprintf("%040x", index+1000)
	}
	_ = repositoryMappingRenameEquivalent(
		t.Context(), cfg,
		repoaudit.RepositoryReviewAutomation{ID: "rra_rename_cap", Repository: fixture.directory},
		repoaudit.RepositoryState{
			LastCommitSHA:      fixture.rename,
			RepositoryFindings: []repoaudit.RepositoryFinding{{FoundCommits: manyCommits}},
		},
	)
}

func TestRepositoryReviewCanonicalMappingValidationResidualSeams(t *testing.T) {
	missingStore := repoaudit.NewSQLiteStore(t.TempDir())
	if _, err := processRepositoryMappingJobs(
		missingStore, t.Context(), "owner/missing", repoaudit.RepositoryMappingProcessOptions{},
	); err == nil {
		t.Fatal("default mapping processor accepted missing canonical ledger")
	}
	if _, err := processRepositoryValidationJobs(
		missingStore, t.Context(), "owner/missing", repoaudit.RepositoryValidationProcessOptions{},
	); err == nil {
		t.Fatal("default validation processor accepted missing canonical ledger")
	}
	if _, err := runRepositoryMappingAdjudication(
		t.Context(), nil, repoaudit.RepositoryMappingModelSnapshot{}, repoaudit.RepositoryMappingAIRequest{},
	); err == nil {
		t.Fatal("nil mapping adjudicator succeeded")
	}
	if _, err := runRepositoryValidationAdjudication(
		t.Context(), nil, repoaudit.RepositoryMappingModelSnapshot{}, repoaudit.RepositoryFinding{}, nil,
	); err == nil {
		t.Fatal("nil validation adjudicator succeeded")
	}

	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	automation.ProfileID = ""
	automation.ReviewerModels = []string{"cheap"}
	automation.AccountRef = "api"
	store := repoaudit.NewSQLiteStore(workspace)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller := &repositoryReviewController{leasedStore: store, leasedConfig: cfg}
	if err = controller.processRepositoryFindingMappings(t.Context(), nil); err != nil {
		t.Fatalf("unowned pending mapping returned error: %v", err)
	}
	missingProfile := automation
	missingProfile.ProfileID = "rrpf_missing"
	if err = controller.processRepositoryFindingMappings(
		t.Context(), []repoaudit.RepositoryReviewAutomation{missingProfile},
	); err != nil {
		t.Fatalf("missing-profile mapping returned error: %v", err)
	}

	invalidRoot := filepath.Join(t.TempDir(), "invalid-git-root")
	if err = os.WriteFile(invalidRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidCfg := *cfg
	invalidCfg.GitWorkspaces.RootDir = invalidRoot
	controller.leasedConfig = &invalidCfg
	originalMappingProcessor := processRepositoryMappingJobs
	originalValidationProcessor := processRepositoryValidationJobs
	t.Cleanup(func() {
		processRepositoryMappingJobs = originalMappingProcessor
		processRepositoryValidationJobs = originalValidationProcessor
	})
	mappingCalls := 0
	processRepositoryMappingJobs = func(
		_ repoaudit.Store,
		ctx context.Context,
		_ string,
		options repoaudit.RepositoryMappingProcessOptions,
	) (repoaudit.RepositoryMappingProcessResult, error) {
		mappingCalls++
		if _, adjudicationErr := options.Adjudicate(
			ctx, options.ModelSnapshot, repoaudit.RepositoryMappingAIRequest{},
		); adjudicationErr == nil {
			t.Error("nil controller mapping adjudicator succeeded")
		}
		return repoaudit.RepositoryMappingProcessResult{}, errors.New("injected canonical mapping failure")
	}
	if err = controller.processRepositoryFindingMappings(
		t.Context(), []repoaudit.RepositoryReviewAutomation{automation},
	); err == nil || mappingCalls != 1 {
		t.Fatalf("mapping callback calls=%d err=%v", mappingCalls, err)
	}
	processRepositoryMappingJobs = originalMappingProcessor

	mapped := completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	controller.leasedConfig = cfg
	if err = controller.processRepositoryFindingMappings(
		t.Context(), []repoaudit.RepositoryReviewAutomation{automation},
	); err != nil {
		t.Fatalf("settled mappings returned error: %v", err)
	}
	if err = controller.processRepositoryFindingValidations(
		t.Context(), []repoaudit.RepositoryReviewAutomation{automation},
	); err != nil {
		t.Fatalf("ledger without validations returned error: %v", err)
	}
	if _, jobs, reserveErr := store.ReserveValidationJobs(
		mapped.Repository,
		[]string{mapped.RepositoryFindings[0].ID},
		repoaudit.RepositoryMappingModelSnapshot{Model: "cheap", Account: "api", Prompt: "validation"},
	); reserveErr != nil || len(jobs) != 1 {
		t.Fatalf("reserve validation jobs=%#v err=%v", jobs, reserveErr)
	}
	if err = controller.processRepositoryFindingValidations(t.Context(), nil); err != nil {
		t.Fatalf("unowned pending validation returned error: %v", err)
	}
	validationCalls := 0
	processRepositoryValidationJobs = func(
		_ repoaudit.Store,
		ctx context.Context,
		_ string,
		options repoaudit.RepositoryValidationProcessOptions,
	) (repoaudit.RepositoryValidationProcessResult, error) {
		validationCalls++
		if _, adjudicationErr := options.Adjudicate(
			ctx, repoaudit.RepositoryMappingModelSnapshot{}, repoaudit.RepositoryFinding{}, nil,
		); adjudicationErr == nil {
			t.Error("nil controller validation adjudicator succeeded")
		}
		reachable, ancestryErr := options.VerifyAncestry(ctx, strings.Repeat("f", 40))
		if ancestryErr != nil || reachable {
			t.Errorf("missing validation metadata reachability=%v err=%v", reachable, ancestryErr)
		}
		return repoaudit.RepositoryValidationProcessResult{}, errors.New("injected canonical validation failure")
	}
	if err = controller.processRepositoryFindingValidations(
		t.Context(), []repoaudit.RepositoryReviewAutomation{automation},
	); err == nil || validationCalls != 1 {
		t.Fatalf("validation callback calls=%d err=%v", validationCalls, err)
	}
}
