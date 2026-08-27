package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewProfileRoutesCRUDAndAssignmentFences(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)

	created := createRepositoryReviewProfileForTest(t, mux, "Focused review", "cheap")
	if created.ID == "" || created.Version != 1 || created.ReviewerModel != "cheap" ||
		created.IssueWriterModel != "" ||
		created.IssuePrompt != repoaudit.DefaultRepositoryReviewIssuePrompt || !created.AutoContinue {
		t.Fatalf("created profile=%#v", created)
	}
	legacyPriceBody := repositoryReviewProfileCreateBody("Legacy price", "cheap")
	legacyPriceBody["model_price"] = map[string]any{
		"input_price_per_1m":  1.0,
		"output_price_per_1m": 4.0,
	}
	legacyPrice := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles", legacyPriceBody,
	)
	if legacyPrice.Code != http.StatusBadRequest {
		t.Fatalf("legacy model_price status=%d body=%s", legacyPrice.Code, legacyPrice.Body.String())
	}

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/repository-reviews/profiles", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), created.ID) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	collectionList := httptest.NewRecorder()
	mux.ServeHTTP(
		collectionList,
		httptest.NewRequest(
			http.MethodGet,
			"/api/repository-reviews/profiles?query=ORDER+BY+name+ASC&limit=1",
			nil,
		),
	)
	var collectionPage struct {
		Profiles       []repoaudit.RepositoryReviewProfile `json:"profiles"`
		Total          int                                 `json:"total"`
		NextCursor     string                              `json:"next_cursor"`
		CanonicalQuery string                              `json:"canonical_query"`
		QuerySchema    map[string]any                      `json:"query_schema"`
	}
	if collectionList.Code != http.StatusOK ||
		json.Unmarshal(collectionList.Body.Bytes(), &collectionPage) != nil ||
		len(collectionPage.Profiles) != 1 || collectionPage.Total != 1 ||
		collectionPage.CanonicalQuery != "ALL ORDER BY name ASC" || collectionPage.QuerySchema == nil {
		t.Fatalf(
			"collection list status=%d page=%#v body=%s",
			collectionList.Code,
			collectionPage,
			collectionList.Body.String(),
		)
	}
	get := httptest.NewRecorder()
	mux.ServeHTTP(
		get,
		httptest.NewRequest(http.MethodGet, "/api/repository-reviews/profiles/"+created.ID, nil),
	)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), created.Name) {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}

	updateBody := repositoryReviewProfileBody(created)
	updateBody["name"] = "Focused review v2"
	updateBody["issue_writer_model"] = "quality"
	updateBody["issue_prompt"] = "Use compact evidence and impact sections."
	updateBody["expected_version"] = created.Version
	updatedResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+created.ID, updateBody,
	)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updatedResponse.Code, updatedResponse.Body.String())
	}
	var updatedResult struct {
		Profile repoaudit.RepositoryReviewProfile `json:"profile"`
	}
	if err := json.Unmarshal(updatedResponse.Body.Bytes(), &updatedResult); err != nil {
		t.Fatal(err)
	}
	updated := updatedResult.Profile
	if updated.Version != 2 || updated.Name != "Focused review v2" ||
		updated.IssueWriterModel != "quality" ||
		updated.IssuePrompt != "Use compact evidence and impact sections." {
		t.Fatalf("updated profile=%#v", updated)
	}
	legacyUpdateBody := repositoryReviewProfileBody(updated)
	delete(legacyUpdateBody, "issue_prompt")
	legacyUpdateBody["name"] = "Focused review v3"
	legacyUpdateBody["expected_version"] = updated.Version
	legacyUpdateResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+created.ID, legacyUpdateBody,
	)
	if legacyUpdateResponse.Code != http.StatusOK {
		t.Fatalf(
			"prompt-compatible update status=%d body=%s",
			legacyUpdateResponse.Code,
			legacyUpdateResponse.Body.String(),
		)
	}
	if err := json.Unmarshal(legacyUpdateResponse.Body.Bytes(), &updatedResult); err != nil {
		t.Fatal(err)
	}
	updated = updatedResult.Profile
	if updated.Version != 3 || updated.Name != "Focused review v3" ||
		updated.IssuePrompt != "Use compact evidence and impact sections." {
		t.Fatalf("omitted issue prompt was not preserved: %#v", updated)
	}

	createAutomation := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "https://github.com/acme/profiled.git",
			"branch":     "",
			"profile_id": updated.ID,
		},
	)
	if createAutomation.Code != http.StatusCreated {
		t.Fatalf("assigned create status=%d body=%s", createAutomation.Code, createAutomation.Body.String())
	}
	var assignedResult struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(createAutomation.Body.Bytes(), &assignedResult); err != nil {
		t.Fatal(err)
	}
	assigned := assignedResult.Automation
	if assigned.ProfileID != updated.ID || assigned.ProfileVersion != updated.Version ||
		assigned.Ref != "" || assigned.Target != "all" ||
		len(assigned.ReviewerModels) != 1 || assigned.ReviewerModels[0] != "cheap" ||
		assigned.IssueWriterModel != "quality" ||
		!strings.Contains(assigned.Name, updated.Name) || !strings.Contains(assigned.Name, "profiled") {
		t.Fatalf("assigned automation=%#v", assigned)
	}
	assignedUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/automations/"+assigned.ID,
		map[string]any{
			"repository":       assigned.Repository,
			"branch":           "main",
			"profile_id":       updated.ID,
			"expected_version": assigned.Version,
		},
	)
	if assignedUpdate.Code != http.StatusOK {
		t.Fatalf("assigned update status=%d body=%s", assignedUpdate.Code, assignedUpdate.Body.String())
	}
	if err := json.Unmarshal(assignedUpdate.Body.Bytes(), &assignedResult); err != nil {
		t.Fatal(err)
	}
	assigned = assignedResult.Automation
	if assigned.Ref != "main" || assigned.ProfileVersion != updated.Version {
		t.Fatalf("updated assignment=%#v", assigned)
	}
	legacyUpdate := automationConfigBody(assigned)
	legacyUpdate["expected_version"] = assigned.Version
	missingAssignment := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/automations/"+assigned.ID,
		legacyUpdate,
	)
	if missingAssignment.Code != http.StatusBadRequest {
		t.Fatalf("missing assignment status=%d body=%s", missingAssignment.Code, missingAssignment.Body.String())
	}

	duplicate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "ACME/PROFILED",
			"branch":     "main",
			"profile_id": updated.ID,
		},
	)
	if duplicate.Code != http.StatusConflict ||
		!strings.Contains(duplicate.Body.String(), "repository_review_repository_assigned") {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}

	assignedDelete := repositoryReviewAutomationMutation(
		t, mux, http.MethodDelete, "/api/repository-reviews/profiles/"+updated.ID,
		map[string]any{"expected_version": updated.Version},
	)
	if assignedDelete.Code != http.StatusConflict ||
		!strings.Contains(assignedDelete.Body.String(), "repository_review_profile_assigned") {
		t.Fatalf("assigned delete status=%d body=%s", assignedDelete.Code, assignedDelete.Body.String())
	}

	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.UpdateAutomation(
		t.Context(), assigned.ID, assigned.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.Status = repoaudit.RepositoryReviewAutomationRunning
			candidate.ActiveRunID = "wr_profile_active"
			candidate.RunIDs = []string{candidate.ActiveRunID}
			candidate.Progress.TotalBatches = 1
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	activeUpdateBody := repositoryReviewProfileBody(updated)
	activeUpdateBody["name"] = "Must wait"
	activeUpdateBody["expected_version"] = updated.Version
	activeUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+updated.ID,
		activeUpdateBody,
	)
	if activeUpdate.Code != http.StatusConflict ||
		!strings.Contains(activeUpdate.Body.String(), "repository_review_profile_active") {
		t.Fatalf(
			"active update status=%d body=%s automation=%#v",
			activeUpdate.Code,
			activeUpdate.Body.String(),
			running,
		)
	}
	stopping, err := store.UpdateAutomation(
		t.Context(), running.ID, running.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.Status = repoaudit.RepositoryReviewAutomationStopping
			candidate.RequestedPauseReason = repoaudit.RepositoryReviewPauseManual
			candidate.RequestedPauseDetail = "Stopping at checkpoint."
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	stoppingUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+updated.ID,
		activeUpdateBody,
	)
	if stoppingUpdate.Code != http.StatusConflict ||
		!strings.Contains(stoppingUpdate.Body.String(), "repository_review_profile_active") {
		t.Fatalf(
			"stopping update status=%d body=%s automation=%#v",
			stoppingUpdate.Code,
			stoppingUpdate.Body.String(),
			stopping,
		)
	}

	unassigned := createRepositoryReviewProfileForTest(t, mux, "Disposable", "cheap")
	deleted := repositoryReviewAutomationMutation(
		t, mux, http.MethodDelete, "/api/repository-reviews/profiles/"+unassigned.ID,
		map[string]any{"expected_version": unassigned.Version},
	)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("unassigned delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestRepositoryReviewCostBudgetRequiresCentralPricingAtMutationAndAdmission(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)

	pricedBody := repositoryReviewProfileCreateBody("Priced review", "cheap")
	pricedBody["budget"] = map[string]any{
		"guard_expression": "spend.total.usd < 10",
	}
	createdResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles", pricedBody,
	)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("priced profile create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		Profile repoaudit.RepositoryReviewProfile `json:"profile"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	automationResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "https://github.com/acme/priced.git",
			"profile_id": created.Profile.ID,
		},
	)
	if automationResponse.Code != http.StatusCreated {
		t.Fatalf(
			"priced automation create status=%d body=%s",
			automationResponse.Code,
			automationResponse.Body.String(),
		)
	}
	var assigned struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(automationResponse.Body.Bytes(), &assigned); err != nil {
		t.Fatal(err)
	}
	price := assigned.Automation.ModelPrices["cheap"]
	if price.InputPricePer1M != 1 || price.OutputPricePer1M != 2 {
		t.Fatalf("server accounting snapshot=%#v", assigned.Automation.ModelPrices)
	}

	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelList[0].InputPricePerMTok = 0
	cfg.ModelList[0].OutputPricePerMTok = 0
	if saveErr := config.SaveConfig(handler.configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}

	unknownCreate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles",
		func() map[string]any {
			body := repositoryReviewProfileCreateBody("Unknown price", "cheap")
			body["budget"] = map[string]any{
				"guard_expression": "spend.total.usd < 1",
			}
			return body
		}(),
	)
	if unknownCreate.Code != http.StatusBadRequest {
		t.Fatalf("unknown-price create status=%d body=%s", unknownCreate.Code, unknownCreate.Body.String())
	}
	updateBody := repositoryReviewProfileBody(created.Profile)
	updateBody["expected_version"] = created.Profile.Version
	updateBody["name"] = "Still centrally priced"
	unknownUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch,
		"/api/repository-reviews/profiles/"+created.Profile.ID,
		updateBody,
	)
	if unknownUpdate.Code != http.StatusBadRequest {
		t.Fatalf("unknown-price update status=%d body=%s", unknownUpdate.Code, unknownUpdate.Body.String())
	}
	unknownAssignment := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "https://github.com/acme/unpriced.git",
			"profile_id": created.Profile.ID,
		},
	)
	if unknownAssignment.Code != http.StatusBadRequest {
		t.Fatalf(
			"unknown-price assignment status=%d body=%s",
			unknownAssignment.Code,
			unknownAssignment.Body.String(),
		)
	}
	unknownAssignmentUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch,
		"/api/repository-reviews/automations/"+assigned.Automation.ID,
		map[string]any{
			"repository":       assigned.Automation.Repository,
			"profile_id":       created.Profile.ID,
			"expected_version": assigned.Automation.Version,
		},
	)
	if unknownAssignmentUpdate.Code != http.StatusBadRequest {
		t.Fatalf(
			"unknown-price assignment update status=%d body=%s",
			unknownAssignmentUpdate.Code,
			unknownAssignmentUpdate.Body.String(),
		)
	}
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	latestProfile, err := store.UpdateProfile(
		t.Context(),
		created.Profile.ID,
		created.Profile.Version,
		func(candidate *repoaudit.RepositoryReviewProfile) error {
			candidate.Name = "Latest centrally governed profile"
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, refreshErr := newRepositoryReviewController(handler).materializeLatestRepositoryReviewProfile(
		t.Context(),
		store,
		assigned.Automation,
	); refreshErr == nil {
		t.Fatal("stale profile snapshot accepted unknown central pricing")
	}
	brokenConfigPath := filepath.Join(t.TempDir(), "broken-config.json")
	if err := os.WriteFile(brokenConfigPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenHandler := NewHandler(brokenConfigPath)
	stale := assigned.Automation
	stale.ProfileVersion = latestProfile.Version - 1
	if _, refreshErr := newRepositoryReviewController(brokenHandler).materializeLatestRepositoryReviewProfile(
		t.Context(),
		store,
		stale,
	); refreshErr == nil {
		t.Fatal("profile materialization accepted an unreadable central configuration")
	}
	unknownAdmission := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+assigned.Automation.ID+"/start",
		map[string]any{"expected_version": assigned.Automation.Version},
	)
	if unknownAdmission.Code != http.StatusBadRequest {
		t.Fatalf("unknown-price admission status=%d body=%s", unknownAdmission.Code, unknownAdmission.Body.String())
	}
}

func TestRepositoryReviewProfileSelectsOneExecutionAccount(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: "secondary", Provider: "openai", Model: "openai/test",
		Enabled: true, InputPricePerMTok: 3, OutputPricePerMTok: 5,
	})
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	body := repositoryReviewProfileCreateBody("Secondary account", "cheap")
	body["account_ref"] = "secondary"
	body["budget"] = map[string]any{"guard_expression": "spend.total.usd < 5"}
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles", body,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("profile status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Profile repoaudit.RepositoryReviewProfile `json:"profile"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Profile.AccountRef != "secondary" ||
		created.Profile.BudgetPolicy.GuardExpression != "spend.total.usd < 5" {
		t.Fatalf("profile=%#v", created.Profile)
	}
	invalid := repositoryReviewProfileCreateBody("Missing account", "cheap")
	invalid["account_ref"] = "missing"
	rejected := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles", invalid,
	)
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "account_ref") {
		t.Fatalf("missing account status=%d body=%s", rejected.Code, rejected.Body.String())
	}
}

func TestRepositoryReviewProfileAssignmentRejectsRevisionAndStrictPayload(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profile := createRepositoryReviewProfileForTest(t, mux, "Branch only", "cheap")
	legacyCreate := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations",
		map[string]any{
			"repository":      "https://github.com/acme/legacy-create.git",
			"ref":             "main",
			"reviewer_models": []string{"cheap", "quality"},
			"compare_models":  true,
		},
	)
	if legacyCreate.Code != http.StatusBadRequest ||
		!strings.Contains(legacyCreate.Body.String(), "profile_id is required") {
		t.Fatalf("legacy create status=%d body=%s", legacyCreate.Code, legacyCreate.Body.String())
	}

	for _, branch := range []string{
		"HEAD",
		strings.Repeat("a", 40),
		"refs/heads/main",
		"v1.2.3^{commit}",
	} {
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
				"repository": "https://github.com/acme/branch-" + strings.ReplaceAll(branch[:3], "/", "-"),
				"branch":     branch,
				"profile_id": profile.ID,
			},
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("branch %q status=%d body=%s", branch, response.Code, response.Body.String())
		}
	}
	for _, profileID := range []string{"invalid", "rrpf_missing"} {
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
				"repository": "https://github.com/acme/missing-profile.git",
				"branch":     "main",
				"profile_id": profileID,
			},
		)
		if response.Code != map[bool]int{true: http.StatusBadRequest, false: http.StatusNotFound}[profileID == "invalid"] {
			t.Fatalf("profile %q status=%d body=%s", profileID, response.Code, response.Body.String())
		}
	}
	invalidUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/automations/rra_missing", map[string]any{
			"repository":       "https://github.com/acme/invalid-update.git",
			"branch":           strings.Repeat("b", 40),
			"profile_id":       profile.ID,
			"expected_version": 1,
		},
	)
	if invalidUpdate.Code != http.StatusBadRequest {
		t.Fatalf("invalid update status=%d body=%s", invalidUpdate.Code, invalidUpdate.Body.String())
	}
	shorthand := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "Example/Short",
			"branch":     "main",
			"profile_id": profile.ID,
		},
	)
	if shorthand.Code != http.StatusCreated ||
		!strings.Contains(shorthand.Body.String(), `"repository":"https://github.com/Example/Short.git"`) {
		t.Fatalf("shorthand status=%d body=%s", shorthand.Code, shorthand.Body.String())
	}
	relative := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "relative/path/repository",
			"branch":     "main",
			"profile_id": profile.ID,
		},
	)
	if relative.Code != http.StatusBadRequest {
		t.Fatalf("relative status=%d body=%s", relative.Code, relative.Body.String())
	}

	strictBody := repositoryReviewProfileBody(profile)
	strictBody["unexpected"] = true
	strict := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles", strictBody,
	)
	if strict.Code != http.StatusBadRequest ||
		!strings.Contains(strict.Body.String(), "invalid_repository_review_profile") {
		t.Fatalf("strict status=%d body=%s", strict.Code, strict.Body.String())
	}
	if repositoryReviewWorkflowRef("") != "" ||
		repositoryReviewWorkflowRef("release/v2") != "refs/heads/release/v2" {
		t.Fatal("repository review workflow ref did not stay branch-only")
	}
	branchCases := []struct {
		request repositoryReviewAutomationConfigRequest
		want    string
		valid   bool
	}{
		{request: repositoryReviewAutomationConfigRequest{Branch: "main", Ref: "main"}, want: "main", valid: true},
		{request: repositoryReviewAutomationConfigRequest{Branch: "bad ref", Ref: "main"}},
		{request: repositoryReviewAutomationConfigRequest{Branch: "main", Ref: "bad ref"}},
		{request: repositoryReviewAutomationConfigRequest{Branch: "main", Ref: "release"}},
		{request: repositoryReviewAutomationConfigRequest{Ref: "HEAD"}},
		{request: repositoryReviewAutomationConfigRequest{Branch: "HEAD"}},
	}
	for _, branchCase := range branchCases {
		got, err := repositoryReviewBranchFromRequest(branchCase.request)
		if branchCase.valid && (err != nil || got != branchCase.want) {
			t.Fatalf("branch request=%#v got=%q err=%v", branchCase.request, got, err)
		}
		if !branchCase.valid && err == nil {
			t.Fatalf("invalid branch request accepted: %#v", branchCase.request)
		}
	}
	if repositoryReviewAssignedAutomationName("", "Profile") != "Profile" ||
		repositoryReviewAssignedAutomationName("owner/repo", "") != "owner/repo" ||
		repositoryReviewAssignedAutomationName("owner/repo", "Profile") != "owner/repo · Profile" {
		t.Fatal("assigned automation name projection failed")
	}
	applyRepositoryReviewMaterializedPolicy(nil, repoaudit.RepositoryReviewAutomation{})
	repositoryCases := []struct {
		input string
		want  string
		valid bool
	}{
		{input: "owner/repo", want: "https://github.com/owner/repo.git", valid: true},
		{input: "owner/repo.git", want: "https://github.com/owner/repo.git", valid: true},
		{input: "/tmp/../tmp/repo", want: "/tmp/repo", valid: true},
		{input: "HTTPS://GitHub.COM/owner/repo.git", want: "https://github.com/owner/repo.git", valid: true},
		{input: "https://gitlab.com/group/repo", want: "https://gitlab.com/group/repo.git", valid: true},
		{input: "git://GitLab.COM/group/sub/repo.git", want: "git://gitlab.com/group/sub/repo.git", valid: true},
		{input: "ssh://git@GitHub.COM/owner/repo.git", want: "ssh://git@github.com/owner/repo.git", valid: true},
		{input: "git@GitHub.COM:owner/repo.git", want: "git@github.com:owner/repo.git", valid: true},
		{input: "git@gitlab.com:group/sub/repo.git", want: "git@gitlab.com:group/sub/repo.git", valid: true},
		{input: "https://gitlab.com/group/sub/repo.git", want: "https://gitlab.com/group/sub/repo.git", valid: true},
		{input: ""},
		{input: " owner/repo"},
		{input: "relative"},
		{input: "owner/repo/extra"},
		{input: "owner/bad repo"},
		{input: "owner/.git"},
		{input: "file://host/repo"},
		{input: "https://github.com/%zz"},
		{input: "https://user@github.com/owner/repo"},
		{input: "ssh://git:secret@github.com/owner/repo"},
		{input: "https://github.com/owner/repo?ref=main"},
		{input: "https://github.com/owner/../repo"},
		{input: "https://github.com/owner/repo/tree/main"},
		{input: "https://github.com"},
		{input: "https://github.com./owner/repo"},
		{input: "https://example.com/repo"},
		{input: "https://gitlab.com:8443/group/repo"},
		{input: "ssh://git@gitlab.com:2222/group/repo"},
		{input: "root@github.com:owner/repo"},
		{input: "git@bad_host:owner/repo"},
		{input: "git@github.com:owner/repo/extra"},
		{input: "git@gitlab.com:../repo"},
		{input: "git@gitlab.com:~/repo"},
		{input: "git@gitlab.com:group/bad repo"},
	}
	for _, repositoryCase := range repositoryCases {
		got, err := normalizeRepositoryReviewAutomationRepository(repositoryCase.input)
		if repositoryCase.valid && (err != nil || got != repositoryCase.want) {
			t.Fatalf("repository %q got=%q want=%q err=%v", repositoryCase.input, got, repositoryCase.want, err)
		}
		if !repositoryCase.valid && err == nil {
			t.Fatalf("invalid repository accepted: %q => %q", repositoryCase.input, got)
		}
	}
	if validRepositoryReviewGitHubSegment("") || validRepositoryReviewGitHubSegment("..") ||
		validRepositoryReviewGitHubSegment("bad space") || !validRepositoryReviewGitHubSegment("good.repo-1") {
		t.Fatal("GitHub repository segment validation failed")
	}
	if validRepositoryReviewRemoteHost("") || validRepositoryReviewRemoteHost(".host") ||
		validRepositoryReviewRemoteHost("host.") || validRepositoryReviewRemoteHost("bad_host") ||
		!validRepositoryReviewRemoteHost("git.example.com") {
		t.Fatal("repository remote host validation failed")
	}
}

func TestRepositoryReviewStartMaterializesLatestProfileAndResetsCampaign(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profile := createRepositoryReviewProfileForTest(t, mux, "Initial", "cheap")
	createdResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "https://github.com/acme/latest-profile.git",
			"branch":     "release/v2",
			"profile_id": profile.ID,
		},
	)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var createdResult struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &createdResult); err != nil {
		t.Fatal(err)
	}
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.UpdateAutomation(
		t.Context(), createdResult.Automation.ID, createdResult.Automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.RunIDs = []string{"wr_old"}
			candidate.Progress = repoaudit.RepositoryReviewProgress{CompletedBatches: 3, TotalBatches: 3}
			candidate.Usage = repoaudit.RepositoryReviewTokenUsage{TotalTokens: 700}
			candidate.ModelStats = map[string]repoaudit.RepositoryReviewModelStats{
				"cheap": {Requests: 3},
			}
			candidate.StartedAt = time.Now().Add(-time.Hour)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	profileBody := repositoryReviewProfileBody(profile)
	profileBody["name"] = "Latest"
	profileBody["review_focus"] = "Use the newest focus."
	profileBody["reviewer_model"] = "quality"
	profileBody["expected_version"] = profile.Version
	updatedResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+profile.ID, profileBody,
	)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("profile update status=%d body=%s", updatedResponse.Code, updatedResponse.Body.String())
	}
	var updatedResult struct {
		Profile repoaudit.RepositoryReviewProfile `json:"profile"`
	}
	if err := json.Unmarshal(updatedResponse.Body.Bytes(), &updatedResult); err != nil {
		t.Fatal(err)
	}

	controller := handler.repositoryReviewControllerInstance()
	received := make(chan repoaudit.RepositoryReviewAutomation, 1)
	controller.runBatch = func(
		_ context.Context,
		materialized repoaudit.RepositoryReviewAutomation,
		runID string,
		_ workflows.AgentUsageObserver,
	) (*workflows.RunResult, error) {
		received <- materialized
		return &workflows.RunResult{
			RunID: runID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 0},
		}, nil
	}
	start := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations/"+automation.ID+"/start",
		map[string]any{"expected_version": automation.Version},
	)
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	select {
	case materialized := <-received:
		if materialized.ProfileVersion != updatedResult.Profile.Version ||
			materialized.ReviewFocus != "Use the newest focus." ||
			len(materialized.ReviewerModels) != 1 || materialized.ReviewerModels[0] != "quality" ||
			materialized.Ref != "release/v2" || materialized.Target != "all" ||
			len(materialized.RunIDs) != 2 || materialized.RunIDs[0] != "wr_old" ||
			materialized.Usage.TotalTokens != 0 || materialized.Progress.CompletedBatches != 0 ||
			len(materialized.ModelStats) != 0 {
			t.Fatalf("latest materialized automation=%#v", materialized)
		}
	case <-time.After(time.Second):
		t.Fatal("started review did not receive latest profile snapshot")
	}
}

func TestRepositoryReviewProfileHandlerFailureBoundaries(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)

	missing := httptest.NewRecorder()
	mux.ServeHTTP(
		missing,
		httptest.NewRequest(http.MethodGet, "/api/repository-reviews/profiles/rrpf_missing", nil),
	)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing get status=%d body=%s", missing.Code, missing.Body.String())
	}
	invalid := httptest.NewRecorder()
	mux.ServeHTTP(
		invalid,
		httptest.NewRequest(http.MethodGet, "/api/repository-reviews/profiles/invalid", nil),
	)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid get status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	for _, mutation := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/repository-reviews/profiles"},
		{method: http.MethodPatch, path: "/api/repository-reviews/profiles/rrpf_missing"},
		{method: http.MethodDelete, path: "/api/repository-reviews/profiles/rrpf_missing"},
	} {
		withoutHeaders := httptest.NewRecorder()
		mux.ServeHTTP(
			withoutHeaders,
			httptest.NewRequest(mutation.method, mutation.path, strings.NewReader(`{}`)),
		)
		if withoutHeaders.Code != http.StatusBadRequest {
			t.Fatalf("unguarded %s status=%d body=%s", mutation.path, withoutHeaders.Code, withoutHeaders.Body.String())
		}

		malformedRequest := httptest.NewRequest(mutation.method, mutation.path, strings.NewReader(`{`))
		setRepositoryReviewMutationHeaders(malformedRequest)
		malformed := httptest.NewRecorder()
		mux.ServeHTTP(malformed, malformedRequest)
		if malformed.Code != http.StatusBadRequest {
			t.Fatalf("malformed %s status=%d body=%s", mutation.path, malformed.Code, malformed.Body.String())
		}
	}

	semanticCreate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles",
		map[string]any{"reviewer_model": "cheap"},
	)
	if semanticCreate.Code != http.StatusBadRequest {
		t.Fatalf("semantic create status=%d body=%s", semanticCreate.Code, semanticCreate.Body.String())
	}
	missingUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/rrpf_missing",
		map[string]any{"reviewer_model": "cheap", "expected_version": 1},
	)
	if missingUpdate.Code != http.StatusNotFound {
		t.Fatalf("missing update status=%d body=%s", missingUpdate.Code, missingUpdate.Body.String())
	}
	missingDelete := repositoryReviewAutomationMutation(
		t, mux, http.MethodDelete, "/api/repository-reviews/profiles/rrpf_missing",
		map[string]any{"expected_version": 1},
	)
	if missingDelete.Code != http.StatusNotFound {
		t.Fatalf("missing delete status=%d body=%s", missingDelete.Code, missingDelete.Body.String())
	}

	defaulted := repositoryReviewProfileFromRequest(repositoryReviewProfileConfigRequest{})
	if !defaulted.AutoContinue {
		t.Fatal("profile auto-continue default was not applied")
	}
	applyRepositoryReviewProfileRequest(nil, repositoryReviewProfileConfigRequest{})
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureRepositoryReviewProfileInactive(t.Context(), store, "rrpf_unassigned"); err != nil {
		t.Fatalf("unassigned inactive check=%v", err)
	}

	root := filepath.Join(workspace, "repository_reviews")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptList := httptest.NewRecorder()
	mux.ServeHTTP(
		corruptList,
		httptest.NewRequest(http.MethodGet, "/api/repository-reviews/profiles", nil),
	)
	if corruptList.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt list status=%d body=%s", corruptList.Code, corruptList.Body.String())
	}
	if err := ensureRepositoryReviewProfileInactive(t.Context(), store, "rrpf_unassigned"); err == nil {
		t.Fatal("corrupt inactive check succeeded")
	}

	missingConfigHandler := NewHandler(t.TempDir())
	t.Cleanup(missingConfigHandler.Shutdown)
	missingConfigMux := http.NewServeMux()
	missingConfigHandler.RegisterRoutes(missingConfigMux)
	for _, request := range []struct {
		method string
		path   string
		body   map[string]any
	}{
		{method: http.MethodGet, path: "/api/repository-reviews/profiles"},
		{method: http.MethodGet, path: "/api/repository-reviews/profiles/rrpf_missing"},
		{method: http.MethodPost, path: "/api/repository-reviews/profiles", body: map[string]any{}},
		{method: http.MethodPatch, path: "/api/repository-reviews/profiles/rrpf_missing", body: map[string]any{}},
		{method: http.MethodDelete, path: "/api/repository-reviews/profiles/rrpf_missing", body: map[string]any{}},
	} {
		var response *httptest.ResponseRecorder
		if request.method == http.MethodGet {
			response = httptest.NewRecorder()
			missingConfigMux.ServeHTTP(
				response, httptest.NewRequest(request.method, request.path, nil),
			)
		} else {
			response = repositoryReviewAutomationMutation(
				t, missingConfigMux, request.method, request.path, request.body,
			)
		}
		if response.Code != http.StatusInternalServerError ||
			!strings.Contains(response.Body.String(), "repository review profile unavailable") {
			t.Fatalf(
				"missing config %s %s status=%d body=%s",
				request.method,
				request.path,
				response.Code,
				response.Body.String(),
			)
		}
	}
}

func TestRepositoryReviewProfileStaleUpdateError(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profile := createRepositoryReviewProfileForTest(t, mux, "Stale", "cheap")
	body := repositoryReviewProfileBody(profile)
	body["expected_version"] = profile.Version
	first := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+profile.ID, body,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first update status=%d body=%s", first.Code, first.Body.String())
	}
	stale := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+profile.ID, body,
	)
	if stale.Code != http.StatusConflict ||
		!strings.Contains(stale.Body.String(), "stale_repository_review_profile") {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func TestRepositoryReviewProfileControllerRefreshBoundaries(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profile := createRepositoryReviewProfileForTest(t, mux, "Refresh", "cheap")
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryReviewController(handler)

	legacy := testRepositoryReviewAutomation()
	if got, refreshErr := controller.materializeLatestRepositoryReviewProfile(
		t.Context(), store, legacy,
	); refreshErr != nil || got.ProfileID != "" {
		t.Fatalf("legacy refresh=%#v err=%v", got, refreshErr)
	}
	for _, profileID := range []string{"invalid", "rrpf_missing"} {
		candidate := testRepositoryReviewAutomation()
		candidate.ProfileID = profileID
		if _, refreshErr := controller.materializeLatestRepositoryReviewProfile(
			t.Context(), store, candidate,
		); refreshErr == nil {
			t.Fatalf("profile %q refresh succeeded", profileID)
		}
	}

	materialized, err := repoaudit.MaterializeRepositoryReviewAutomation(
		profile, testRepositoryReviewAutomation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	materialized.Name = repositoryReviewAssignedAutomationName(materialized.Repository, profile.Name)
	created, err := store.CreateAutomation(t.Context(), materialized)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureRepositoryReviewProfileInactive(t.Context(), store, "rrpf_other"); err != nil {
		t.Fatalf("other profile inactive check=%v", err)
	}
	if got, refreshErr := controller.materializeLatestRepositoryReviewProfile(
		t.Context(), store, created,
	); refreshErr != nil || got.Version != created.Version {
		t.Fatalf("current refresh=%#v err=%v", got, refreshErr)
	}

	stale := created
	stale.ProfileVersion--
	injected := errors.New("injected update failure")
	controller.update = func(
		context.Context,
		repoaudit.Store,
		string,
		int64,
		func(*repoaudit.RepositoryReviewAutomation) error,
	) (repoaudit.RepositoryReviewAutomation, error) {
		return repoaudit.RepositoryReviewAutomation{}, injected
	}
	if _, refreshErr := controller.materializeLatestRepositoryReviewProfile(
		t.Context(), store, stale,
	); !errors.Is(refreshErr, injected) {
		t.Fatalf("injected refresh error=%v", refreshErr)
	}
	controller.update = func(
		_ context.Context,
		_ repoaudit.Store,
		_ string,
		_ int64,
		mutate func(*repoaudit.RepositoryReviewAutomation) error,
	) (repoaudit.RepositoryReviewAutomation, error) {
		invalidBranch := created
		invalidBranch.Ref = strings.Repeat("c", 40)
		return repoaudit.RepositoryReviewAutomation{}, mutate(&invalidBranch)
	}
	if _, refreshErr := controller.materializeLatestRepositoryReviewProfile(
		t.Context(), store, stale,
	); !errors.Is(refreshErr, repoaudit.ErrInvalidAutomation) {
		t.Fatalf("materialization refresh error=%v", refreshErr)
	}
	invalidSnapshot := stale
	invalidSnapshot.Ref = strings.Repeat("d", 40)
	if _, refreshErr := newRepositoryReviewController(handler).materializeLatestRepositoryReviewProfile(
		t.Context(), store, invalidSnapshot,
	); !errors.Is(refreshErr, repoaudit.ErrInvalidAutomation) {
		t.Fatalf("invalid stored snapshot error=%v", refreshErr)
	}
	normalizationController := newRepositoryReviewController(handler)
	normalizationController.update = func(
		context.Context,
		repoaudit.Store,
		string,
		int64,
		func(*repoaudit.RepositoryReviewAutomation) error,
	) (repoaudit.RepositoryReviewAutomation, error) {
		return repoaudit.RepositoryReviewAutomation{}, injected
	}
	legacyHEAD := testRepositoryReviewAutomation()
	legacyHEAD.Ref = "HEAD"
	legacyHEAD.Target = "changed"
	if _, normalizeErr := normalizationController.normalizeRepositoryReviewAutomationAdmission(
		t.Context(), store, legacyHEAD,
	); !errors.Is(normalizeErr, injected) {
		t.Fatalf("injected normalization error=%v", normalizeErr)
	}
	resetRepositoryReviewExecutionCampaign(nil)

	missingProfilePath := filepath.Join(
		workspace, "repository_reviews", "profile_"+profile.ID+".json",
	)
	if err := os.Remove(missingProfilePath); err != nil {
		t.Fatal(err)
	}
	start := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations/"+created.ID+"/start",
		map[string]any{"expected_version": created.Version},
	)
	if start.Code != http.StatusNotFound {
		t.Fatalf("missing profile start status=%d body=%s", start.Code, start.Body.String())
	}
}

func TestRepositoryReviewProfileRejectsUnsafeModelAliases(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	validProfile := createRepositoryReviewProfileForTest(t, mux, "Initially safe", "cheap")
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelAliases = append(cfg.ModelAliases,
		config.ModelAliasConfig{Name: "agentic", Model: "codex-cli/gpt-5"},
		config.ModelAliasConfig{
			Name: "writer-override", Model: "codex-cli/gpt-5",
			AccountOverrides: map[string]string{"api": "openai/gpt-5"},
		},
		config.ModelAliasConfig{
			Name: "disabled", Model: "openai/disabled",
			DisabledAccounts: []string{cfg.Agents.Defaults.AccountRef},
		},
	)
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	writerOverride := repositoryReviewProfileCreateBody("Account-safe writer", "cheap")
	writerOverride["issue_writer_model"] = "writer-override"
	writerResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles", writerOverride,
	)
	if writerResponse.Code != http.StatusCreated {
		t.Fatalf(
			"account-safe writer status=%d body=%s",
			writerResponse.Code,
			writerResponse.Body.String(),
		)
	}
	optionsResponse := httptest.NewRecorder()
	mux.ServeHTTP(
		optionsResponse,
		httptest.NewRequest(http.MethodGet, "/api/repository-reviews/automation-options", nil),
	)
	var options struct {
		Accounts []repositoryReviewAccountOption `json:"accounts"`
	}
	if optionsResponse.Code != http.StatusOK || json.Unmarshal(optionsResponse.Body.Bytes(), &options) != nil {
		t.Fatalf("writer options status=%d body=%s", optionsResponse.Code, optionsResponse.Body.String())
	}
	writerSelectable, reviewerSelectable := false, false
	for _, account := range options.Accounts {
		if account.ID != "api" {
			continue
		}
		for _, alias := range account.WriterModels {
			writerSelectable = writerSelectable || alias == "writer-override"
		}
		for _, alias := range account.Models {
			reviewerSelectable = reviewerSelectable || alias == "writer-override"
		}
	}
	if !writerSelectable || reviewerSelectable {
		t.Fatalf("writer options=%#v", options.Accounts)
	}
	for _, model := range []string{"missing", "agentic", "disabled"} {
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, "/api/repository-reviews/profiles",
			repositoryReviewProfileCreateBody("Unsafe "+model, model),
		)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), "invalid_repository_review_profile") {
			t.Fatalf("model %q status=%d body=%s", model, response.Code, response.Body.String())
		}
		writerBody := repositoryReviewProfileCreateBody("Unsafe writer "+model, "cheap")
		writerBody["issue_writer_model"] = model
		writerResponse := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, "/api/repository-reviews/profiles", writerBody,
		)
		if writerResponse.Code != http.StatusBadRequest ||
			!strings.Contains(writerResponse.Body.String(), "invalid_repository_review_profile") {
			t.Fatalf(
				"issue writer %q status=%d body=%s",
				model,
				writerResponse.Code,
				writerResponse.Body.String(),
			)
		}
	}
	updateBody := repositoryReviewProfileBody(validProfile)
	updateBody["reviewer_model"] = "agentic"
	updateBody["expected_version"] = validProfile.Version
	update := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+validProfile.ID,
		updateBody,
	)
	if update.Code != http.StatusBadRequest {
		t.Fatalf("unsafe model update status=%d body=%s", update.Code, update.Body.String())
	}
	missingConfig := NewHandler(t.TempDir())
	if err := missingConfig.validateRepositoryReviewProfileSelection(
		"", "cheap", repoaudit.RepositoryReviewBudgetPolicy{},
	); err == nil {
		t.Fatal("missing config model validation succeeded")
	}
}

func TestRepositoryReviewAdmissionRejectsWriterAliasDrift(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profileBody := repositoryReviewProfileCreateBody("Writer admission", "cheap")
	profileBody["issue_writer_model"] = "quality"
	profileResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles", profileBody,
	)
	if profileResponse.Code != http.StatusCreated {
		t.Fatalf("profile status=%d body=%s", profileResponse.Code, profileResponse.Body.String())
	}
	var profileResult struct {
		Profile repoaudit.RepositoryReviewProfile `json:"profile"`
	}
	if err := json.Unmarshal(profileResponse.Body.Bytes(), &profileResult); err != nil {
		t.Fatal(err)
	}
	automationResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "https://github.com/acme/writer-admission.git",
			"profile_id": profileResult.Profile.ID,
		},
	)
	if automationResponse.Code != http.StatusCreated {
		t.Fatalf(
			"automation status=%d body=%s",
			automationResponse.Code, automationResponse.Body.String(),
		)
	}
	var automationResult struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(automationResponse.Body.Bytes(), &automationResult); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for index := range cfg.ModelAliases {
		if cfg.ModelAliases[index].Name == "quality" {
			cfg.ModelAliases[index].DisabledAccounts = append(
				cfg.ModelAliases[index].DisabledAccounts, "api",
			)
		}
	}
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	start := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automationResult.Automation.ID+"/start",
		map[string]any{"expected_version": automationResult.Automation.Version},
	)
	if start.Code != http.StatusBadRequest ||
		!strings.Contains(start.Body.String(), "issue_writer_model") {
		t.Fatalf("writer drift start status=%d body=%s", start.Code, start.Body.String())
	}
}

func TestRepositoryReviewProfileAcceptsDefaultCredentialRouterAndAvailableDirectCredential(t *testing.T) {
	withPicoclawAuthHome(t)
	setOpenAIAuthCredential(
		t,
		"openai:work",
		"access-token",
		"refresh-token",
		"account-work",
		"work@example.test",
	)
	if err := auth.SetCredential("github-copilot:gh-copilot", &auth.AuthCredential{
		AccessToken: "ghp_invalid-copilot-token",
		Provider:    "github-copilot",
		AuthMethod:  "token",
	}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "review"
	cfg.Agents.Defaults.AccountRef = "review-router"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name: "review", Model: "gpt-review",
		AccountOverrides: map[string]string{
			"credential:github-copilot:gh-copilot": "gpt-review-copilot",
		},
	}}
	cfg.ModelList = nil
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name: "review-router", Enabled: true, Entry: "copilot",
		Blocks: []config.AccountRouterBlock{
			{
				ID: "copilot", Type: config.AccountRouterBlockTypeAccount,
				Account: "credential:github-copilot:gh-copilot", Fallback: "openai",
			},
			{
				ID: "openai", Type: config.AccountRouterBlockTypeLoadBalance,
				Accounts: []string{
					"credential:openai:work",
					"credential:openai:missing",
				},
			},
		},
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	t.Cleanup(handler.Shutdown)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	blankDefault := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/profiles",
		repositoryReviewProfileCreateBody("Default router", "review"),
	)
	if blankDefault.Code != http.StatusCreated {
		t.Fatalf("default-router create status=%d body=%s", blankDefault.Code, blankDefault.Body.String())
	}
	var created struct {
		Profile repoaudit.RepositoryReviewProfile `json:"profile"`
	}
	if err := json.Unmarshal(blankDefault.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	updateBody := repositoryReviewProfileBody(created.Profile)
	updateBody["account_ref"] = "credential:openai:work"
	updateBody["expected_version"] = created.Profile.Version
	updated := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/repository-reviews/profiles/"+created.Profile.ID,
		updateBody,
	)
	if updated.Code != http.StatusOK {
		t.Fatalf("direct-credential update status=%d body=%s", updated.Code, updated.Body.String())
	}

	directBody := repositoryReviewProfileCreateBody("Direct credential", "review")
	directBody["account_ref"] = "credential:openai:work"
	direct := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/profiles",
		directBody,
	)
	if direct.Code != http.StatusCreated {
		t.Fatalf("direct-credential create status=%d body=%s", direct.Code, direct.Body.String())
	}

	missingBody := repositoryReviewProfileCreateBody("Missing credential", "review")
	missingBody["account_ref"] = "credential:openai:missing"
	missing := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/profiles",
		missingBody,
	)
	if missing.Code != http.StatusBadRequest ||
		!strings.Contains(missing.Body.String(), "account_ref") {
		t.Fatalf("missing-credential create status=%d body=%s", missing.Code, missing.Body.String())
	}

	invalidCopilotBody := repositoryReviewProfileCreateBody("Invalid Copilot credential", "review")
	invalidCopilotBody["account_ref"] = "credential:github-copilot:gh-copilot"
	invalidCopilot := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/profiles",
		invalidCopilotBody,
	)
	if invalidCopilot.Code != http.StatusBadRequest ||
		!strings.Contains(invalidCopilot.Body.String(), "account_ref") {
		t.Fatalf(
			"invalid-copilot create status=%d body=%s",
			invalidCopilot.Code,
			invalidCopilot.Body.String(),
		)
	}
}

func TestRepositoryReviewLegacyAdmissionMigratesHEADAndRejectsRevision(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	legacyHEAD := testRepositoryReviewAutomation()
	legacyHEAD.Repository = "acme/legacy-head"
	legacyHEAD.Ref = "hEaD"
	legacyHEAD.Target = "changed"
	legacyHEAD, err = store.CreateAutomation(t.Context(), legacyHEAD)
	if err != nil {
		t.Fatal(err)
	}
	legacyRevision := testRepositoryReviewAutomation()
	legacyRevision.Repository = "https://github.com/acme/legacy-revision.git"
	legacyRevision.Ref = "main~1"
	legacyRevision, err = store.CreateAutomation(t.Context(), legacyRevision)
	if err != nil {
		t.Fatal(err)
	}

	rejected := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+legacyRevision.ID+"/start",
		map[string]any{"expected_version": legacyRevision.Version},
	)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("legacy revision status=%d body=%s", rejected.Code, rejected.Body.String())
	}

	controller := handler.repositoryReviewControllerInstance()
	received := make(chan repoaudit.RepositoryReviewAutomation, 1)
	controller.runBatch = func(
		_ context.Context,
		automation repoaudit.RepositoryReviewAutomation,
		runID string,
		_ workflows.AgentUsageObserver,
	) (*workflows.RunResult, error) {
		received <- automation
		return &workflows.RunResult{
			RunID: runID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 0},
		}, nil
	}
	started := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+legacyHEAD.ID+"/start",
		map[string]any{"expected_version": legacyHEAD.Version},
	)
	if started.Code != http.StatusAccepted {
		t.Fatalf("legacy HEAD status=%d body=%s", started.Code, started.Body.String())
	}
	select {
	case automation := <-received:
		if automation.Repository != "https://github.com/acme/legacy-head.git" ||
			automation.Ref != "" || automation.Target != "all" {
			t.Fatalf("legacy HEAD admission=%#v", automation)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy HEAD review did not start")
	}
}

func TestRepositoryReviewAdmissionRepairsForgedSameVersionProfileSnapshot(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profile := createRepositoryReviewProfileForTest(t, mux, "Canonical", "cheap")
	createdResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "https://github.com/acme/forged-snapshot.git",
			"branch":     "main",
			"profile_id": profile.ID,
		},
	)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var createdResult struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &createdResult); err != nil {
		t.Fatal(err)
	}
	forged := createdResult.Automation
	forged.ReviewFocus = "Forged focus must not run."
	forged.Progress = repoaudit.RepositoryReviewProgress{CompletedBatches: 8, TotalBatches: 8}
	forged.Usage = repoaudit.RepositoryReviewTokenUsage{TotalTokens: 999}
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(
		workspace, "repository_reviews", "automation_"+forged.ID+".json",
	)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	controller := handler.repositoryReviewControllerInstance()
	received := make(chan repoaudit.RepositoryReviewAutomation, 1)
	controller.runBatch = func(
		_ context.Context,
		automation repoaudit.RepositoryReviewAutomation,
		runID string,
		_ workflows.AgentUsageObserver,
	) (*workflows.RunResult, error) {
		received <- automation
		return &workflows.RunResult{
			RunID: runID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 0},
		}, nil
	}
	started := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+forged.ID+"/start",
		map[string]any{"expected_version": forged.Version},
	)
	if started.Code != http.StatusAccepted {
		t.Fatalf("forged start status=%d body=%s", started.Code, started.Body.String())
	}
	select {
	case automation := <-received:
		if automation.ReviewFocus != profile.ReviewFocus ||
			automation.Progress.CompletedBatches != 0 || automation.Usage.TotalTokens != 0 {
			t.Fatalf("forged snapshot admitted=%#v", automation)
		}
	case <-time.After(time.Second):
		t.Fatal("repaired snapshot did not start")
	}
}

func createRepositoryReviewProfileForTest(
	t *testing.T,
	mux *http.ServeMux,
	name string,
	model string,
) repoaudit.RepositoryReviewProfile {
	t.Helper()
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles",
		repositoryReviewProfileCreateBody(name, model),
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("profile create status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Profile repoaudit.RepositoryReviewProfile `json:"profile"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Profile
}

func repositoryReviewProfileCreateBody(name, model string) map[string]any {
	return map[string]any{
		"name": name, "review_focus": "Find correctness and security defects.",
		"issue_prompt":   repoaudit.DefaultRepositoryReviewIssuePrompt,
		"account_ref":    "",
		"scope_policy":   map[string]any{"code_types": []string{"code"}},
		"reviewer_model": model,
		"force":          false, "auto_continue": true,
		"max_files_per_run": 4, "max_content_bytes": 65536,
		"max_parallel_children": 8,
		"budget":                map[string]any{"guard_expression": ""},
	}
}

func repositoryReviewProfileBody(profile repoaudit.RepositoryReviewProfile) map[string]any {
	return map[string]any{
		"name": profile.Name, "review_focus": profile.ReviewFocus,
		"issue_prompt": profile.IssuePrompt,
		"account_ref":  profile.AccountRef,
		"scope_policy": profile.ScopePolicy, "reviewer_model": profile.ReviewerModel,
		"issue_writer_model": profile.IssueWriterModel,
		"force":              profile.Force,
		"auto_continue":      profile.AutoContinue, "max_files_per_run": profile.MaxFilesPerRun,
		"max_content_bytes":     profile.MaxContentBytes,
		"max_parallel_children": profile.MaxParallelChildren,
		"budget":                profile.BudgetPolicy,
	}
}
