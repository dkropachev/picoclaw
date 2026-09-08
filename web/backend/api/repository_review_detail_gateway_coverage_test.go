package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewDirectPostBoundaryOutcomes(t *testing.T) {
	t.Run("request validation", func(t *testing.T) {
		handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		path := "/api/repository-reviews/automations/rra_missing/findings/rf_missing/post"
		requests := []*http.Request{
			httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{`)),
			httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expected_version":0}`)),
			httptest.NewRequest(
				http.MethodPost,
				path,
				strings.NewReader(`{"expected_version":1,"instructions":"\u0000"}`),
			),
		}
		for _, request := range requests {
			setRepositoryReviewMutationHeaders(request)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid direct post=%d %s", response.Code, response.Body.String())
			}
		}
		crossSite := httptest.NewRequest(
			http.MethodPost, "http://launcher.invalid"+path,
			strings.NewReader(`{"expected_version":1}`),
		)
		crossSite.Header.Set("Content-Type", "application/json")
		crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, crossSite)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("cross-site direct post=%d %s", response.Code, response.Body.String())
		}

		missing := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, path, map[string]any{"expected_version": 1},
		)
		if missing.Code != http.StatusNotFound {
			t.Fatalf("missing direct post=%d %s", missing.Code, missing.Body.String())
		}
	})

	t.Run("stale occurrence", func(t *testing.T) {
		_, mux, _, state, automation := newMappedRepositoryReviewDetailFixture(t)
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, repositoryReviewDirectPostPath(automation.ID, state.Findings[0].ID),
			map[string]any{"expected_version": state.Findings[0].Version + 1},
		)
		if response.Code != http.StatusConflict {
			t.Fatalf("stale direct post=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("generation fails", func(t *testing.T) {
		_, mux, _, state, automation := newMappedRepositoryReviewDetailFixture(t)
		previous := runRepositoryReviewIssueWriter
		t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
		runRepositoryReviewIssueWriter = func(
			context.Context, *Handler, repoaudit.RepositoryReviewAutomation,
			repoaudit.Finding, []repoaudit.FindingContext, string, string,
		) (repositoryReviewIssueWriterResult, error) {
			return repositoryReviewIssueWriterResult{}, errors.New("provider unavailable")
		}
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, repositoryReviewDirectPostPath(automation.ID, state.Findings[0].ID),
			map[string]any{"expected_version": state.Findings[0].Version},
		)
		if response.Code != http.StatusBadGateway ||
			!strings.Contains(response.Body.String(), `"code":"generation_failed"`) {
			t.Fatalf("failed generation direct post=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("profile resolution fails", func(t *testing.T) {
		handler, mux, _, state, automation := newMappedRepositoryReviewDetailFixture(t)
		cfg, err := config.LoadConfig(handler.configPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Agents.Defaults.AccountRef = ""
		if err := config.SaveConfig(handler.configPath, cfg); err != nil {
			t.Fatal(err)
		}
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, repositoryReviewDirectPostPath(automation.ID, state.Findings[0].ID),
			map[string]any{"expected_version": state.Findings[0].Version},
		)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("missing profile account direct post=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("generation ID fails", func(t *testing.T) {
		_, mux, _, state, automation := newMappedRepositoryReviewDetailFixture(t)
		previousRandom := readRepositoryReviewIssueGenerationRandom
		t.Cleanup(func() { readRepositoryReviewIssueGenerationRandom = previousRandom })
		readRepositoryReviewIssueGenerationRandom = func([]byte) (int, error) {
			return 0, errors.New("entropy unavailable")
		}
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, repositoryReviewDirectPostPath(automation.ID, state.Findings[0].ID),
			map[string]any{"expected_version": state.Findings[0].Version},
		)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("generation ID direct post=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("publication fails after custom generation", func(t *testing.T) {
		_, mux, _, state, automation := newMappedRepositoryReviewDetailFixture(t)
		previous := runRepositoryReviewIssueWriter
		t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
		runRepositoryReviewIssueWriter = successfulRepositoryReviewCoverageWriter
		installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
			return eventUpstreamResponse(
				http.StatusBadGateway,
				`{"outcome":"failed","code":"provider_failed","message":"publication failed"}`,
			), nil
		})
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, repositoryReviewDirectPostPath(automation.ID, state.Findings[0].ID),
			map[string]any{
				"expected_version": state.Findings[0].Version,
				"instructions":     "Emphasize the observable impact.",
			},
		)
		if response.Code != http.StatusBadGateway ||
			!strings.Contains(response.Body.String(), `"outcome":"failed"`) {
			t.Fatalf("failed publication direct post=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("ledger disappears after publication", func(t *testing.T) {
		_, mux, workspace, state, automation := newMappedRepositoryReviewDetailFixture(t)
		previous := runRepositoryReviewIssueWriter
		t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
		runRepositoryReviewIssueWriter = successfulRepositoryReviewCoverageWriter
		installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
			databasePath := filepath.Join(
				workspace, "repository_reviews", "repository-reviews.db",
			)
			for _, suffix := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(databasePath + suffix)
			}
			return eventUpstreamResponse(http.StatusOK, `{"outcome":"posted"}`), nil
		})
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, repositoryReviewDirectPostPath(automation.ID, state.Findings[0].ID),
			map[string]any{"expected_version": state.Findings[0].Version},
		)
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing final ledger direct post=%d %s", response.Code, response.Body.String())
		}
	})
}

func TestRepositoryReviewAdvertisedDefaultBranchAndGitOutputBoundaries(t *testing.T) {
	if _, err := resolveRepositoryReviewAdvertisedDefaultBranch(
		t.Context(), nil, repoaudit.RepositoryReviewAutomation{},
	); err == nil {
		t.Fatal("nil config default branch resolution succeeded")
	}

	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, loadErr := config.LoadConfig(handler.configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	repository := newRepositoryReviewDefaultBranchGitFixture(t)
	cfg.GitWorkspaces.RootDir = t.TempDir()
	branch, branchErr := resolveRepositoryReviewAdvertisedDefaultBranch(
		t.Context(), cfg, repoaudit.RepositoryReviewAutomation{
			ID: "rra_default_branch", Repository: repository,
		},
	)
	if branchErr != nil || branch != "main" {
		t.Fatalf("default branch=%q err=%v", branch, branchErr)
	}
	blockedRoot := filepath.Join(t.TempDir(), "workspace-root-file")
	if err := os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	blockedConfig := *cfg
	blockedConfig.GitWorkspaces.RootDir = blockedRoot
	if _, err := resolveRepositoryReviewAdvertisedDefaultBranch(
		t.Context(), &blockedConfig, repoaudit.RepositoryReviewAutomation{
			ID: "rra_blocked_default", Repository: repository,
		},
	); err == nil {
		t.Fatal("blocked workspace root resolved a default branch")
	}
	missingRepositoryConfig := *cfg
	missingRepositoryConfig.GitWorkspaces.RootDir = t.TempDir()
	if _, err := resolveRepositoryReviewAdvertisedDefaultBranch(
		t.Context(), &missingRepositoryConfig, repoaudit.RepositoryReviewAutomation{
			ID: "rra_missing_default", Repository: filepath.Join(t.TempDir(), "missing"),
		},
	); err == nil {
		t.Fatal("missing repository resolved a default branch")
	}
	realGit, lookupErr := exec.LookPath("git")
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	wrapperRoot := t.TempDir()
	wrapper := filepath.Join(wrapperRoot, "git")
	wrapperScript := "#!/bin/sh\nif [ \"$1\" = \"symbolic-ref\" ]; then exit 1; fi\nexec \"" +
		realGit + "\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperRoot+string(os.PathListSeparator)+os.Getenv("PATH"))
	noSymbolicRefConfig := *cfg
	noSymbolicRefConfig.GitWorkspaces.RootDir = t.TempDir()
	if _, err := resolveRepositoryReviewAdvertisedDefaultBranch(
		t.Context(), &noSymbolicRefConfig, repoaudit.RepositoryReviewAutomation{
			ID: "rra_no_symbolic_ref", Repository: repository,
		},
	); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("symbolic-ref failure error=%v", err)
	}
	output, gitErr := repositoryReviewGitOutput(
		t.Context(), repository, 3, "git", "rev-parse", "--abbrev-ref", "HEAD",
	)
	if gitErr == nil || string(output) != "mai" {
		t.Fatalf("bounded git output=%q err=%v", output, gitErr)
	}
	if _, err := repositoryReviewGitOutput(
		t.Context(), repository, 10, "git", "rev-parse", "missing-ref",
	); err == nil {
		t.Fatal("failing git command succeeded")
	}
}

func TestRepositoryReviewCurrentIssueProfileErrorAndFallbackBranches(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store := repoaudit.NewStore(workspace)
	baseLedger := repositoryReviewAutomationLedger{
		Store: store,
		Automation: repoaudit.RepositoryReviewAutomation{
			IssueWriterModel: "cheap", AccountRef: "api",
		},
	}
	missing := baseLedger
	missing.Automation.ProfileID = "rrpf_missing"
	if _, err := handler.repositoryReviewCurrentIssueProfile(t.Context(), missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing profile error=%v", err)
	}
	profile, createErr := store.CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		Name: "Fallback writer", ReviewFocus: "Find bugs.", ReviewerModel: "cheap",
		IssuePrompt: "Present the diagnosis.", AccountRef: "api", AutoContinue: true,
		MaxFilesPerRun: 4, MaxContentBytes: 65536, MaxParallelChildren: 1,
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	fallback := baseLedger
	fallback.Automation.ProfileID = profile.ID
	got, resolveErr := handler.repositoryReviewCurrentIssueProfile(t.Context(), fallback)
	if resolveErr != nil || got.Model != profile.ReviewerModel {
		t.Fatalf("fallback writer profile=%#v err=%v", got, resolveErr)
	}

	badConfigPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(badConfigPath, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	badHandler := &Handler{configPath: badConfigPath}
	if _, err := badHandler.repositoryReviewCurrentIssueProfile(t.Context(), fallback); err == nil {
		t.Fatal("corrupt config profile resolution succeeded")
	}

	cfg, configErr := config.LoadConfig(handler.configPath)
	if configErr != nil {
		t.Fatal(configErr)
	}
	cfg.ModelAliases[0].DisabledAccounts = []string{"api"}
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.repositoryReviewCurrentIssueProfile(t.Context(), fallback); err == nil {
		t.Fatal("disabled issue writer alias was accepted")
	}
	unknownModelProfile, unknownCreateErr := store.CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		Name: "Unknown writer", ReviewFocus: "Find bugs.", ReviewerModel: "cheap",
		IssueWriterModel: "not-configured", IssuePrompt: "Present it.", AccountRef: "api",
		AutoContinue: true, MaxFilesPerRun: 4, MaxContentBytes: 65536, MaxParallelChildren: 1,
	})
	if unknownCreateErr != nil {
		t.Fatal(unknownCreateErr)
	}
	unknown := baseLedger
	unknown.Automation.ProfileID = unknownModelProfile.ID
	if _, err := handler.repositoryReviewCurrentIssueProfile(t.Context(), unknown); err == nil {
		t.Fatal("unknown issue writer alias was accepted")
	}
	blockedRoot := filepath.Join(t.TempDir(), "profile-store-file")
	if err := os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	loadFailure := missing
	loadFailure.Store = repoaudit.NewStore(blockedRoot)
	if _, err := handler.repositoryReviewCurrentIssueProfile(t.Context(), loadFailure); err == nil {
		t.Fatal("profile store failure unexpectedly resolved")
	}
}

func TestRepositoryReviewCurrentIssueProfileRejectsBlankEffectiveAccount(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store := repoaudit.NewStore(workspace)
	profile, err := store.CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		Name: "Blank account", ReviewFocus: "Find bugs.", ReviewerModel: "cheap",
		IssuePrompt: "Present it.", AutoContinue: true, MaxFilesPerRun: 4,
		MaxContentBytes: 65536, MaxParallelChildren: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.AccountRef = ""
	cfg.Agents.Defaults.ModelName = ""
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ledger := repositoryReviewAutomationLedger{
		Store: store, Automation: repoaudit.RepositoryReviewAutomation{ProfileID: profile.ID},
	}
	if _, err := handler.repositoryReviewCurrentIssueProfile(t.Context(), ledger); err == nil {
		t.Fatal("blank profile account unexpectedly resolved")
	}
}

func TestRepositoryReviewCurrentIssueProfileRejectsUnknownWriterAlias(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store := repoaudit.NewStore(workspace)
	profile, err := store.CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		Name: "Unknown writer", ReviewFocus: "Find bugs.", ReviewerModel: "cheap",
		IssueWriterModel: "not-configured", IssuePrompt: "Present it.", AccountRef: "api",
		AutoContinue: true, MaxFilesPerRun: 4, MaxContentBytes: 65536, MaxParallelChildren: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger := repositoryReviewAutomationLedger{
		Store: store, Automation: repoaudit.RepositoryReviewAutomation{ProfileID: profile.ID},
	}
	if _, err := handler.repositoryReviewCurrentIssueProfile(t.Context(), ledger); err == nil {
		t.Fatal("unknown issue writer alias unexpectedly resolved")
	}
}

func TestRepositoryReviewControllerStartReconcileFailureAndCanceledReconcile(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	store := repoaudit.NewStore(cfg.WorkspacePath())
	state := seedRepositoryReviewAPIState(t, cfg.WorkspacePath())
	corrupt, found, err := store.Get(state.Repository)
	if err != nil || !found {
		t.Fatal(err)
	}
	corrupt.MappingJobs[0].ReviewFindingID = "rf_missing"
	corrupt.Version++
	corrupt.UpdatedAt = time.Now().UTC()
	_ = corrupt
	statePath := filepath.Join(workspace, "repository_reviews", "repository-reviews.db")
	if err := os.WriteFile(statePath, []byte("not-sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryReviewController(handler)
	if err := controller.Start(); err == nil {
		t.Fatalf("controller reconcile error=%v", err)
	}
	hasLease := controller.releaseLease != nil
	if hasLease || controller.ctx.Err() == nil {
		t.Fatalf(
			"failed controller retained lease=%t context=%v",
			hasLease, controller.ctx.Err(),
		)
	}

	canceled := newRepositoryReviewController(handler)
	canceled.leasedStore = store
	canceled.leasedConfig = cfg
	canceled.cancel()
	canceled.reconcile()
}

func TestRepositoryReviewControllerAdmissionUsesAdvertisedDefaultResolver(t *testing.T) {
	for _, test := range []struct {
		name       string
		resolveErr error
	}{
		{name: "failure", resolveErr: errors.New("default unavailable")},
		{name: "success stops after resolution"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
			t.Cleanup(handler.Shutdown)
			store, err := handler.repositoryReviewStore()
			if err != nil {
				t.Fatal(err)
			}
			automation := testRepositoryReviewAutomation()
			automation.Repository = newRepositoryReviewDefaultBranchGitFixture(t)
			automation.Ref = "main"
			automation, err = store.CreateAutomation(t.Context(), automation)
			if err != nil {
				t.Fatal(err)
			}
			controller := newRepositoryReviewController(handler)
			t.Cleanup(controller.Stop)
			controller.resolveDefaultBranch = func(
				context.Context, *config.Config, repoaudit.RepositoryReviewAutomation,
			) (string, error) {
				return "main", test.resolveErr
			}
			if test.resolveErr == nil {
				controller.update = func(
					context.Context, repoaudit.Store, string, int64,
					func(*repoaudit.RepositoryReviewAutomation) error,
				) (repoaudit.RepositoryReviewAutomation, error) {
					return repoaudit.RepositoryReviewAutomation{}, errors.New("stop after default resolution")
				}
			}
			_, startErr := controller.startAutomationAtCommit(
				t.Context(), automation.ID, automation.Version, false, "start", "",
			)
			if startErr == nil {
				t.Fatal("admission unexpectedly succeeded")
			}
		})
	}
}

func newMappedRepositoryReviewDetailFixture(
	t *testing.T,
) (*Handler, *http.ServeMux, string, repoaudit.RepositoryState, repoaudit.RepositoryReviewAutomation) {
	t.Helper()
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	return handler, mux, workspace, state, automation
}

func repositoryReviewDirectPostPath(automationID, findingID string) string {
	return "/api/repository-reviews/automations/" + automationID +
		"/findings/" + findingID + "/post"
}

func successfulRepositoryReviewCoverageWriter(
	context.Context,
	*Handler,
	repoaudit.RepositoryReviewAutomation,
	repoaudit.Finding,
	[]repoaudit.FindingContext,
	string,
	string,
) (repositoryReviewIssueWriterResult, error) {
	return repositoryReviewIssueWriterResult{
		Title: "Generated issue", Body: "Grounded diagnosis and provenance.", Labels: []string{"bug"},
	}, nil
}

func newRepositoryReviewDefaultBranchGitFixture(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	git := func(arguments ...string) {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "default-branch@example.test")
	git("config", "user.name", "Default Branch Test")
	if err := os.WriteFile(
		filepath.Join(repository, "service.go"), []byte("package service\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	git("add", "service.go")
	git("commit", "-m", "initial")
	return repository
}
