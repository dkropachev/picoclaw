package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/reposcope"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryModelEvaluationRoutesLifecycleAndSafeProjection(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	created := createRepositoryModelEvaluation(t, mux, initAPISourceRepo(t))
	if created.Repository != "" || created.Status != repoeval.StatusDraft {
		t.Fatalf("created projection = %#v", created)
	}

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/model-evaluations", nil))
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "/absolute/checkout") ||
		strings.Contains(listed.Body.String(), "corpus") {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}

	patched := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/model-evaluations/"+created.ID,
		map[string]any{
			"expected_version": created.Version,
			"repository":       "acme/core",
			"ref":              "release",
			"focus": map[string]any{
				"code_types":      []string{"code", "test"},
				"include_folders": []string{"pkg"},
			},
		},
	)
	if patched.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patched.Code, patched.Body.String())
	}
	var detail repositoryModelEvaluationDetail
	if err := json.Unmarshal(patched.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Evaluation.Repository != "https://github.com/acme/core.git" || detail.Evaluation.Ref != "release" {
		t.Fatalf("patched evaluation=%#v", detail.Evaluation)
	}

	stale := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/model-evaluations/"+created.ID,
		map[string]any{
			"expected_version": created.Version, "ref": "stale",
		},
	)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale patch status=%d body=%s", stale.Code, stale.Body.String())
	}

	deleted := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodDelete,
		"/api/model-evaluations/"+created.ID,
		map[string]any{
			"expected_version": detail.Evaluation.Version,
		},
	)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/model-evaluations/"+created.ID, nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestRepositoryModelEvaluationCreateDefaultsBlankRefToHEAD(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	body := repositoryModelEvaluationCreateBody("owner/repo")
	body["ref"] = "   "
	response := repositoryModelEvaluationMutation(t, mux, http.MethodPost, "/api/model-evaluations", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var detail repositoryModelEvaluationDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Evaluation.Ref != "HEAD" {
		t.Fatalf("default ref=%q, want HEAD", detail.Evaluation.Ref)
	}
	if detail.Evaluation.Repository != "https://github.com/owner/repo.git" {
		t.Fatalf("normalized repository=%q", detail.Evaluation.Repository)
	}
	missingLocal := repositoryModelEvaluationCreateBody(filepath.Join(t.TempDir(), "missing"))
	missing := repositoryModelEvaluationMutation(t, mux, http.MethodPost, "/api/model-evaluations", missingLocal)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing local repository status=%d body=%s", missing.Code, missing.Body.String())
	}
	nonGitLocal := repositoryModelEvaluationCreateBody(t.TempDir())
	nonGit := repositoryModelEvaluationMutation(t, mux, http.MethodPost, "/api/model-evaluations", nonGitLocal)
	if nonGit.Code != http.StatusBadRequest {
		t.Fatalf("non-Git local repository status=%d body=%s", nonGit.Code, nonGit.Body.String())
	}
	nonDirectoryPath := filepath.Join(t.TempDir(), "repository.txt")
	if err := os.WriteFile(nonDirectoryPath, []byte("not a repository"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonDirectoryLocal := repositoryModelEvaluationCreateBody(nonDirectoryPath)
	nonDirectory := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations",
		nonDirectoryLocal,
	)
	if nonDirectory.Code != http.StatusBadRequest {
		t.Fatalf("non-directory local repository status=%d body=%s", nonDirectory.Code, nonDirectory.Body.String())
	}
}

func TestRepositoryModelEvaluationGitRootValidation(t *testing.T) {
	repository := initAPISourceRepo(t)
	if !repositoryModelEvaluationGitRoot(nil, repository) {
		t.Fatal("working tree root was rejected")
	}
	nested := filepath.Join(repository, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if repositoryModelEvaluationGitRoot(t.Context(), nested) {
		t.Fatal("working tree subdirectory was accepted")
	}
	bareRepository := t.TempDir()
	runAPIGit(t, bareRepository, "init", "--bare")
	if !repositoryModelEvaluationGitRoot(t.Context(), bareRepository) {
		t.Fatal("bare repository root was rejected")
	}
	if repositoryModelEvaluationGitRoot(t.Context(), t.TempDir()) {
		t.Fatal("non-Git directory was accepted")
	}

	fakeBin := t.TempDir()
	fakeGit := filepath.Join(fakeBin, "git")
	if err := os.WriteFile(fakeGit, []byte(`#!/bin/sh
case "$PROBE_GIT_MODE:$4" in
  invalid:--is-bare-repository) echo maybe; exit 0 ;;
  root-error:--is-bare-repository) echo false; exit 0 ;;
  root-error:--show-toplevel) exit 1 ;;
  missing-root:--is-bare-repository) echo false; exit 0 ;;
  missing-root:--show-toplevel) echo /path/that/does/not/exist; exit 0 ;;
esac
exit 1
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)
	for _, mode := range []string{"invalid", "root-error", "missing-root"} {
		t.Setenv("PROBE_GIT_MODE", mode)
		if repositoryModelEvaluationGitRoot(t.Context(), repository) {
			t.Fatalf("fake Git mode %q was accepted", mode)
		}
	}
}

func TestRepositoryModelEvaluationRoutesRejectUnsafeRequestsModelsAndPages(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)

	requestBody, _ := json.Marshal(repositoryModelEvaluationCreateBody("owner/repo"))
	request := httptest.NewRequest(http.MethodPost, "/api/model-evaluations", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("cross-site status=%d body=%s", response.Code, response.Body.String())
	}

	unsafeBody := repositoryModelEvaluationCreateBody("owner/repo")
	unsafeBody["candidate_models"] = []string{"model-a", "unsafe"}
	unsafe := repositoryModelEvaluationMutation(t, mux, http.MethodPost, "/api/model-evaluations", unsafeBody)
	if unsafe.Code != http.StatusBadRequest {
		t.Fatalf("unsafe alias status=%d body=%s", unsafe.Code, unsafe.Body.String())
	}

	unknownBody := repositoryModelEvaluationCreateBody("owner/repo")
	unknownBody["unexpected"] = true
	unknown := repositoryModelEvaluationMutation(t, mux, http.MethodPost, "/api/model-evaluations", unknownBody)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	created := createRepositoryModelEvaluation(t, mux, "owner/repo")
	for _, suffix := range []string{"?limit=0", "?limit=201", "?offset=-1", "?other=1", "?limit=1&limit=2"} {
		page := httptest.NewRecorder()
		mux.ServeHTTP(
			page,
			httptest.NewRequest(http.MethodGet, "/api/model-evaluations/"+created.ID+"/corpus"+suffix, nil),
		)
		if page.Code != http.StatusBadRequest {
			t.Fatalf("page %q status=%d body=%s", suffix, page.Code, page.Body.String())
		}
	}
	badQuery := httptest.NewRecorder()
	mux.ServeHTTP(badQuery, httptest.NewRequest(http.MethodGet, "/api/model-evaluations?x=1", nil))
	if badQuery.Code != http.StatusBadRequest {
		t.Fatalf("list query status=%d", badQuery.Code)
	}
	if sanitizeRepositoryModelEvaluationIdentity("file:///tmp/secret") != "" ||
		sanitizeRepositoryModelEvaluationIdentity("https://token@example.test/acme/repo.git") != "" ||
		sanitizeRepositoryModelEvaluationIdentity("https://github.com/acme/repo.git") == "" {
		t.Fatal("repository identity sanitization mismatch")
	}
	projectable := repoeval.Evaluation{RunIDs: make([]string, 55)}
	for index := range projectable.RunIDs {
		projectable.RunIDs[index] = fmt.Sprintf("wr_%d", index)
	}
	if projected := projectRepositoryModelEvaluation(projectable); len(projected.RunIDs) != 50 ||
		projected.RunIDs[0] != "wr_5" {
		t.Fatalf("projected run IDs=%v", projected.RunIDs)
	}
}

func TestRepositoryModelEvaluationOptionsExposeOnlySafeModelsAndRepositories(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-evaluations/options", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("options status=%d body=%s", response.Code, response.Body.String())
	}
	var options struct {
		Models    []repositoryReviewModelOption `json:"models"`
		CodeTypes []repoeval.CodeType           `json:"code_types"`
		Default   int                           `json:"default_files_per_language"`
		Maximum   int                           `json:"max_files_per_language"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Models) != 4 || len(options.CodeTypes) != 4 || options.Default != 20 || options.Maximum != 20 {
		t.Fatalf("options=%#v", options)
	}
	for _, option := range options.Models {
		if option.Alias == "unsafe" || !option.Available || option.BlockedReason != "" {
			t.Fatalf("unsafe model leaked into options: %#v", option)
		}
	}
}

func TestRepositoryModelEvaluationRoutesFullPatchResumeAndBusyDelete(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	entered := make(chan struct{})
	controller.runWorkflow = func(
		ctx context.Context,
		_ string,
		_ string,
		_ string,
		_ map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	created := createRepositoryModelEvaluation(t, mux, "owner/repo")

	draftCorpus := httptest.NewRecorder()
	mux.ServeHTTP(
		draftCorpus,
		httptest.NewRequest(http.MethodGet, "/api/model-evaluations/"+created.ID+"/corpus", nil),
	)
	if draftCorpus.Code != http.StatusOK || !strings.Contains(draftCorpus.Body.String(), `"total":0`) {
		t.Fatalf("draft corpus status=%d body=%s", draftCorpus.Code, draftCorpus.Body.String())
	}
	badDetail := httptest.NewRecorder()
	mux.ServeHTTP(
		badDetail,
		httptest.NewRequest(http.MethodGet, "/api/model-evaluations/"+created.ID+"?x=1", nil),
	)
	if badDetail.Code != http.StatusBadRequest {
		t.Fatalf("detail query status=%d", badDetail.Code)
	}

	patched := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/model-evaluations/"+created.ID,
		map[string]any{
			"expected_version":     created.Version,
			"repository":           "https://github.com/acme/other.git",
			"ref":                  "release",
			"candidate_models":     []string{"model-b", "model-a"},
			"selector_model_alias": "selector",
			"judge_model_alias":    "judge",
			"focus": map[string]any{
				"code_types":      []string{"hotpath-code"},
				"include_folders": []string{"cmd"},
				"exclude_folders": []string{"cmd/generated"},
				"free_text":       "focus auth",
			},
			"default_files_per_language": 9,
			"files_per_language":         map[string]int{"go": 7},
		},
	)
	if patched.Code != http.StatusOK {
		t.Fatalf("full patch status=%d body=%s", patched.Code, patched.Body.String())
	}
	var patchedDetail repositoryModelEvaluationDetail
	if err := json.Unmarshal(patched.Body.Bytes(), &patchedDetail); err != nil {
		t.Fatal(err)
	}
	if patchedDetail.Evaluation.DefaultFilesPerLanguage != 9 ||
		patchedDetail.Evaluation.FilesPerLanguage["go"] != 7 ||
		patchedDetail.Evaluation.Focus.FreeText != "focus auth" ||
		patchedDetail.Evaluation.CandidateModels[0] != "model-b" {
		t.Fatalf("patched detail=%#v", patchedDetail.Evaluation)
	}

	startDraft := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/start",
		map[string]any{"expected_version": patchedDetail.Evaluation.Version},
	)
	if startDraft.Code != http.StatusConflict {
		t.Fatalf("start draft status=%d body=%s", startDraft.Code, startDraft.Body.String())
	}
	resumed := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/run",
		map[string]any{"expected_version": patchedDetail.Evaluation.Version},
	)
	if resumed.Code != http.StatusAccepted {
		t.Fatalf("run draft status=%d body=%s", resumed.Code, resumed.Body.String())
	}
	<-entered
	active, _, _ := handler.getRepositoryModelEvaluation(t.Context(), created.ID)
	busyResume := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/resume",
		map[string]any{"expected_version": active.Version},
	)
	if busyResume.Code != http.StatusConflict {
		t.Fatalf("busy resume status=%d body=%s", busyResume.Code, busyResume.Body.String())
	}
	busyDelete := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodDelete,
		"/api/model-evaluations/"+created.ID,
		map[string]any{"expected_version": active.Version},
	)
	if busyDelete.Code != http.StatusConflict {
		t.Fatalf("busy delete status=%d body=%s", busyDelete.Code, busyDelete.Body.String())
	}
	cancel := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/cancel",
		map[string]any{"expected_version": active.Version},
	)
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	terminal := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCanceled)
	staleDelete := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodDelete,
		"/api/model-evaluations/"+created.ID,
		map[string]any{"expected_version": terminal.Version - 1},
	)
	if staleDelete.Code != http.StatusConflict {
		t.Fatalf("stale delete status=%d", staleDelete.Code)
	}
}

func TestRepositoryModelEvaluationControllerCompletesDeterministicWorkflow(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := newRepositoryModelEvaluationController(handler)
	var mu sync.Mutex
	var refs []string
	controller.runWorkflow = func(ctx context.Context, _ string, ref string, runID string, _ map[string]any, observe workflows.AgentUsageEventObserver) (*workflows.RunResult, error) {
		mu.Lock()
		refs = append(refs, ref)
		mu.Unlock()
		switch ref {
		case workflows.RepositoryModelEvaluationPreflightWorkflowRef:
			_ = observe(
				workflows.AgentUsageEvent{RunID: runID, StepID: "selector", Usage: workflows.AgentUsage{
					Model:            "gpt-selector",
					Reviewer:         "selector",
					PromptTokens:     10,
					CompletionTokens: 5,
				}},
			)
			return repositoryModelEvaluationPreflightResult(), nil
		case workflows.RepositoryModelEvaluationBatchWorkflowRef:
			_ = observe(
				workflows.AgentUsageEvent{RunID: runID, StepID: "candidates", Usage: workflows.AgentUsage{
					Model:            "gpt-a",
					Reviewer:         "model-a",
					PromptTokens:     100,
					CompletionTokens: 20,
					LatencyMillis:    12,
				}},
			)
			_ = observe(
				workflows.AgentUsageEvent{RunID: runID, StepID: "candidates", Usage: workflows.AgentUsage{
					Model:            "gpt-b",
					Reviewer:         "model-b",
					PromptTokens:     110,
					CompletionTokens: 25,
					LatencyMillis:    14,
				}},
			)
			return repositoryModelEvaluationBatchResult(), nil
		case workflows.RepositoryModelEvaluationAnalysisWorkflowRef:
			return repositoryModelEvaluationAnalysisResult(), nil
		default:
			return nil, context.Canceled
		}
	}
	handler.repositoryModelEvaluationController = controller

	created := createRepositoryModelEvaluation(t, mux, "https://github.com/acme/core.git")
	preflight := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/preflight",
		map[string]any{"expected_version": created.Version},
	)
	if preflight.Code != http.StatusAccepted {
		t.Fatalf("preflight status=%d body=%s", preflight.Code, preflight.Body.String())
	}
	var accepted repositoryModelEvaluationDetail
	if err := json.Unmarshal(
		preflight.Body.Bytes(),
		&accepted,
	); err != nil ||
		accepted.Evaluation.Status != repoeval.StatusPreflighting {
		t.Fatalf("accepted preflight=%#v err=%v", accepted, err)
	}
	ready := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusReady)
	if ready.Corpus == nil || len(ready.Corpus.Files) != 2 || ready.Corpus.LanguageCounts["go"] != 1 ||
		ready.Corpus.LanguageCounts["typescript"] != 1 {
		t.Fatalf("ready evaluation=%#v", ready)
	}
	detailResponse := httptest.NewRecorder()
	mux.ServeHTTP(detailResponse, httptest.NewRequest(http.MethodGet, "/api/model-evaluations/"+created.ID, nil))
	var projected repositoryModelEvaluationDetail
	if err := json.Unmarshal(
		detailResponse.Body.Bytes(),
		&projected,
	); err != nil || projected.Evaluation.Corpus == nil ||
		len(projected.Evaluation.Corpus.Files) != 0 ||
		projected.Evaluation.Corpus.SelectionRationale == "" {
		t.Fatalf("projected ready detail=%#v err=%v", projected, err)
	}
	start := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/start",
		map[string]any{"expected_version": ready.Version},
	)
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	completed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCompleted)
	if len(completed.Comparisons) != 2 || completed.Comparisons[0].ModelAlias != "model-a" ||
		completed.Comparisons[0].Rank != 1 ||
		completed.Usage.Requests != 3 ||
		completed.ModelStats["model-a"].FilesCompleted != 2 ||
		completed.Checkpoint.ConcreteModels["model-a"]["gpt-a"] != 1 {
		t.Fatalf("completed evaluation=%#v", completed)
	}
	if _, err := controller.Resume(t.Context(), completed.ID, completed.Version); !errors.Is(
		err,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("completed resume error=%v", err)
	}
	if _, err := controller.Restart(t.Context(), completed.ID, completed.Version); !errors.Is(
		err,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("completed restart error=%v", err)
	}
	deleteCompleted := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodDelete,
		"/api/model-evaluations/"+completed.ID,
		map[string]any{"expected_version": completed.Version},
	)
	if deleteCompleted.Code != http.StatusConflict {
		t.Fatalf(
			"completed delete status=%d body=%s",
			deleteCompleted.Code,
			deleteCompleted.Body.String(),
		)
	}
	corpus := httptest.NewRecorder()
	mux.ServeHTTP(
		corpus,
		httptest.NewRequest(http.MethodGet, "/api/model-evaluations/"+created.ID+"/corpus?offset=0&limit=1", nil),
	)
	if corpus.Code != http.StatusOK || strings.Contains(corpus.Body.String(), "source") ||
		!strings.Contains(corpus.Body.String(), "next_offset") {
		t.Fatalf("corpus status=%d body=%s", corpus.Code, corpus.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(refs) != 3 || refs[0] != workflows.RepositoryModelEvaluationPreflightWorkflowRef ||
		refs[2] != workflows.RepositoryModelEvaluationAnalysisWorkflowRef {
		t.Fatalf("workflow refs=%v", refs)
	}
}

func TestRepositoryModelEvaluationOneShotRunCompletesWithoutReadyAction(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	var mu sync.Mutex
	var refs []string
	controller.runWorkflow = func(
		_ context.Context,
		_ string,
		ref string,
		_ string,
		_ map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		mu.Lock()
		refs = append(refs, ref)
		mu.Unlock()
		switch ref {
		case workflows.RepositoryModelEvaluationPreflightWorkflowRef:
			return repositoryModelEvaluationPreflightResult(), nil
		case workflows.RepositoryModelEvaluationBatchWorkflowRef:
			return repositoryModelEvaluationBatchResult(), nil
		case workflows.RepositoryModelEvaluationAnalysisWorkflowRef:
			return repositoryModelEvaluationAnalysisResult(), nil
		default:
			return nil, errors.New("unexpected workflow")
		}
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	response := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/run",
		repositoryModelEvaluationCreateBody("owner/one-shot"),
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("run status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted repositoryModelEvaluationDetail
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil ||
		!accepted.Evaluation.OneShot || accepted.Evaluation.Status != repoeval.StatusPreflighting {
		t.Fatalf("accepted=%#v err=%v", accepted.Evaluation, err)
	}
	completed := waitRepositoryModelEvaluationStatus(t, handler, accepted.Evaluation.ID, repoeval.StatusCompleted)
	mu.Lock()
	defer mu.Unlock()
	if !completed.OneShot || len(completed.Comparisons) != len(completed.CandidateModels) ||
		!reflect.DeepEqual(refs, []string{
			workflows.RepositoryModelEvaluationPreflightWorkflowRef,
			workflows.RepositoryModelEvaluationBatchWorkflowRef,
			workflows.RepositoryModelEvaluationAnalysisWorkflowRef,
		}) {
		t.Fatalf("completed=%#v refs=%v", completed, refs)
	}
}

func TestRepositoryModelEvaluationControllerCancellationAndRestart(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := newRepositoryModelEvaluationController(handler)
	entered := make(chan struct{})
	controller.runWorkflow = func(ctx context.Context, _ string, _ string, _ string, _ map[string]any, _ workflows.AgentUsageEventObserver) (*workflows.RunResult, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	handler.repositoryModelEvaluationController = controller
	created := createRepositoryModelEvaluation(t, mux, "owner/repo")
	preflight := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/preflight",
		map[string]any{"expected_version": created.Version},
	)
	if preflight.Code != http.StatusAccepted {
		t.Fatalf("preflight status=%d", preflight.Code)
	}
	<-entered
	current, _, _ := handler.getRepositoryModelEvaluation(t.Context(), created.ID)
	canceled := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/cancel",
		map[string]any{"expected_version": current.Version},
	)
	if canceled.Code != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", canceled.Code, canceled.Body.String())
	}
	var canceledDetail repositoryModelEvaluationDetail
	if err := json.Unmarshal(
		canceled.Body.Bytes(),
		&canceledDetail,
	); err != nil ||
		canceledDetail.Evaluation.Status != repoeval.StatusCanceled {
		t.Fatalf("cancel response=%#v err=%v", canceledDetail, err)
	}
	terminal := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCanceled)
	resumed := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/resume",
		map[string]any{"expected_version": terminal.Version},
	)
	if resumed.Code != http.StatusConflict {
		t.Fatalf("terminal resume status=%d body=%s", resumed.Code, resumed.Body.String())
	}
	restarted := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/restart",
		map[string]any{"expected_version": terminal.Version},
	)
	if restarted.Code != http.StatusConflict {
		t.Fatalf("restart status=%d body=%s", restarted.Code, restarted.Body.String())
	}
}

func TestRepositoryModelEvaluationFailedRestartCreatesFreshProbe(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	entered := make(chan struct{})
	controller.runWorkflow = func(
		ctx context.Context,
		_ string,
		ref string,
		_ string,
		_ map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		if ref != workflows.RepositoryModelEvaluationPreflightWorkflowRef {
			return nil, errors.New("unexpected restart workflow")
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	draft, err := store.Create(
		t.Context(),
		repositoryModelEvaluationCreateRequest("owner/failed-restart"),
	)
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := store.Update(
		t.Context(),
		draft.ID,
		draft.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusPreflighting
			candidate.Progress.Stage = repoeval.ProgressResolving
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.Update(
		t.Context(),
		preflighting.ID,
		preflighting.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusFailed
			candidate.Progress.Stage = repoeval.ProgressFailed
			candidate.Failure = "repository clone failed"
			return nil
		},
	)
	if err != nil || failed.Corpus != nil {
		t.Fatalf("failed preflight=%#v err=%v", failed, err)
	}

	restart := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+failed.ID+"/restart",
		map[string]any{"expected_version": failed.Version},
	)
	if restart.Code != http.StatusAccepted {
		t.Fatalf("failed restart status=%d body=%s", restart.Code, restart.Body.String())
	}
	var detail repositoryModelEvaluationDetail
	if decodeErr := json.Unmarshal(restart.Body.Bytes(), &detail); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if detail.Evaluation.ID == failed.ID || detail.Evaluation.Status != repoeval.StatusPreflighting ||
		detail.Evaluation.Repository != "https://github.com/owner/failed-restart.git" {
		t.Fatalf("failed restart evaluation=%#v", detail.Evaluation)
	}
	<-entered
	original, found, err := store.Get(t.Context(), failed.ID)
	if err != nil || !found || original.Status != repoeval.StatusFailed {
		t.Fatalf("original failed probe=%#v found=%v err=%v", original, found, err)
	}
}

func TestRepositoryModelEvaluationFailedPreflightRestartsSameRunToCompletion(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = func(
		ctx context.Context,
		workflowYAML string,
		ref string,
		runID string,
		inputs map[string]any,
		observe workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		if ref == workflows.RepositoryModelEvaluationPreflightWorkflowRef {
			return repositoryModelEvaluationPreflightResult(), nil
		}
		return successfulRepositoryModelEvaluationWorkflow(
			ctx,
			workflowYAML,
			ref,
			runID,
			inputs,
			observe,
		)
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	draft, err := store.Create(
		t.Context(),
		repositoryModelEvaluationCreateRequest("owner/failed-retry"),
	)
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := store.Update(
		t.Context(),
		draft.ID,
		draft.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusPreflighting
			candidate.Progress.Stage = repoeval.ProgressResolving
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.Update(
		t.Context(),
		preflighting.ID,
		preflighting.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusFailed
			candidate.Progress.Stage = repoeval.ProgressFailed
			candidate.Failure = "selector deadline exceeded"
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	restart := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+failed.ID+"/resume",
		map[string]any{"expected_version": failed.Version},
	)
	if restart.Code != http.StatusAccepted {
		t.Fatalf("same-run restart status=%d body=%s", restart.Code, restart.Body.String())
	}
	var accepted repositoryModelEvaluationDetail
	if decodeErr := json.Unmarshal(restart.Body.Bytes(), &accepted); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if accepted.Evaluation.ID != failed.ID || !accepted.Evaluation.OneShot ||
		accepted.Evaluation.Status != repoeval.StatusPreflighting {
		t.Fatalf("same-run restart=%#v", accepted.Evaluation)
	}
	completed := waitRepositoryModelEvaluationStatus(t, handler, failed.ID, repoeval.StatusCompleted)
	if len(completed.Comparisons) != len(completed.CandidateModels) {
		t.Fatalf("same-run completed=%#v", completed)
	}
}

func TestRepositoryModelEvaluationAPIRejectsStrictRequestBoundaries(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	created := createRepositoryModelEvaluation(t, mux, "owner/repo")
	badOptionsQuery := httptest.NewRecorder()
	mux.ServeHTTP(
		badOptionsQuery,
		httptest.NewRequest(http.MethodGet, "/api/model-evaluations/options?x=1", nil),
	)
	if badOptionsQuery.Code != http.StatusBadRequest {
		t.Fatalf("options query status=%d body=%s", badOptionsQuery.Code, badOptionsQuery.Body.String())
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "patch query", method: http.MethodPatch, path: "/api/model-evaluations/" + created.ID + "?x=1", body: `{"expected_version":1}`},
		{name: "delete query", method: http.MethodDelete, path: "/api/model-evaluations/" + created.ID + "?x=1", body: `{"expected_version":1}`},
		{name: "action query", method: http.MethodPost, path: "/api/model-evaluations/" + created.ID + "/preflight?x=1", body: `{"expected_version":1}`},
		{name: "patch missing version", method: http.MethodPatch, path: "/api/model-evaluations/" + created.ID, body: `{}`},
		{name: "delete missing version", method: http.MethodDelete, path: "/api/model-evaluations/" + created.ID, body: `{}`},
		{name: "action missing version", method: http.MethodPost, path: "/api/model-evaluations/" + created.ID + "/preflight", body: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Sec-Fetch-Site", "same-origin")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "nil request", mutate: nil},
		{name: "nil URL", mutate: func(r *http.Request) { r.URL = nil }},
		{name: "query", mutate: func(r *http.Request) { r.URL.RawQuery = "x=1" }},
		{name: "nil body", mutate: func(r *http.Request) { r.Body = nil }},
		{name: "oversized declared body", mutate: func(r *http.Request) {
			r.ContentLength = repositoryModelEvaluationRequestMaxBytes + 1
		}},
	} {
		t.Run("decode "+test.name, func(t *testing.T) {
			var request *http.Request
			if test.mutate != nil {
				request = httptest.NewRequest(http.MethodPost, "/api/model-evaluations", strings.NewReader(`{}`))
				test.mutate(request)
			}
			var target repositoryModelEvaluationActionRequest
			if err := decodeRepositoryModelEvaluationRequest(request, &target); err == nil {
				t.Fatal("decode accepted invalid request")
			}
		})
	}

	trailing := httptest.NewRequest(
		http.MethodPost,
		"/api/model-evaluations",
		strings.NewReader(`{"expected_version":1} {"second":true}`),
	)
	var trailingTarget repositoryModelEvaluationActionRequest
	if err := decodeRepositoryModelEvaluationRequest(trailing, &trailingTarget); err == nil {
		t.Fatal("decode accepted a second JSON value")
	}

	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "nil request", mutate: nil},
		{name: "nil URL", mutate: func(r *http.Request) { r.URL = nil }},
		{name: "query", mutate: func(r *http.Request) { r.URL.RawQuery = "x=1" }},
		{name: "cross origin", mutate: func(r *http.Request) {
			r.Header.Del("Sec-Fetch-Site")
			r.Header.Set("Origin", "https://attacker.example")
		}},
		{name: "missing content type", mutate: func(r *http.Request) { r.Header.Del("Content-Type") }},
		{name: "encoded body", mutate: func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
	} {
		t.Run("mutation "+test.name, func(t *testing.T) {
			var request *http.Request
			if test.mutate != nil {
				request = httptest.NewRequest(http.MethodPost, "/api/model-evaluations", strings.NewReader(`{}`))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Sec-Fetch-Site", "same-origin")
				test.mutate(request)
			}
			if err := validateRepositoryModelEvaluationMutation(request); err == nil {
				t.Fatal("mutation accepted invalid request")
			}
		})
	}
}

func TestRepositoryModelEvaluationAPIConfigStoreAndMissingFailures(t *testing.T) {
	brokenConfigPath := filepath.Join(t.TempDir(), "broken-config.json")
	if err := os.WriteFile(brokenConfigPath, []byte(`{"agents":`), 0o600); err != nil {
		t.Fatal(err)
	}
	missingConfigHandler := NewHandler(brokenConfigPath)
	missingConfigMux := http.NewServeMux()
	missingConfigHandler.RegisterRoutes(missingConfigMux)
	t.Cleanup(missingConfigHandler.Shutdown)

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{name: "list config", method: http.MethodGet, path: "/api/model-evaluations"},
		{name: "create config", method: http.MethodPost, path: "/api/model-evaluations", body: repositoryModelEvaluationCreateBody("owner/repo")},
		{name: "get config", method: http.MethodGet, path: "/api/model-evaluations/" + repositoryModelEvaluationMissingID()},
		{name: "patch config", method: http.MethodPatch, path: "/api/model-evaluations/" + repositoryModelEvaluationMissingID(), body: map[string]any{"expected_version": 1}},
		{name: "delete config", method: http.MethodDelete, path: "/api/model-evaluations/" + repositoryModelEvaluationMissingID(), body: map[string]any{"expected_version": 1}},
		{name: "action controller config", method: http.MethodPost, path: "/api/model-evaluations/" + repositoryModelEvaluationMissingID() + "/preflight", body: map[string]any{"expected_version": 1}},
		{name: "corpus config", method: http.MethodGet, path: "/api/model-evaluations/" + repositoryModelEvaluationMissingID() + "/corpus"},
		{name: "options config", method: http.MethodGet, path: "/api/model-evaluations/options"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := repositoryModelEvaluationRequest(t, missingConfigMux, test.method, test.path, test.body)
			if response.Code != http.StatusBadRequest ||
				strings.Contains(response.Body.String(), brokenConfigPath) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	handler, mux, workspace := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	missingID := repositoryModelEvaluationMissingID()
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{name: "patch missing", method: http.MethodPatch, path: "/api/model-evaluations/" + missingID, body: map[string]any{"expected_version": 1}},
		{name: "delete missing", method: http.MethodDelete, path: "/api/model-evaluations/" + missingID, body: map[string]any{"expected_version": 1}},
		{name: "corpus missing", method: http.MethodGet, path: "/api/model-evaluations/" + missingID + "/corpus"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := repositoryModelEvaluationRequest(t, mux, test.method, test.path, test.body)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	invalidCreate := repositoryModelEvaluationCreateBody("owner/repo")
	invalidCreate["candidate_models"] = []string{"model-a"}
	invalidCreateResponse := repositoryModelEvaluationRequest(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations",
		invalidCreate,
	)
	if invalidCreateResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid create status=%d body=%s", invalidCreateResponse.Code, invalidCreateResponse.Body.String())
	}

	created := createRepositoryModelEvaluation(t, mux, "owner/repo")
	unsafePatch := repositoryModelEvaluationRequest(
		t,
		mux,
		http.MethodPatch,
		"/api/model-evaluations/"+created.ID,
		map[string]any{"expected_version": created.Version, "candidate_models": []string{"model-a", "unsafe"}},
	)
	if unsafePatch.Code != http.StatusBadRequest {
		t.Fatalf("unsafe patch status=%d body=%s", unsafePatch.Code, unsafePatch.Body.String())
	}

	root := filepath.Join(workspace, "repository_evaluations")
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{name: "list store", method: http.MethodGet, path: "/api/model-evaluations"},
		{name: "create store", method: http.MethodPost, path: "/api/model-evaluations", body: repositoryModelEvaluationCreateBody("owner/repo")},
		{name: "get store", method: http.MethodGet, path: "/api/model-evaluations/" + created.ID},
		{name: "patch store", method: http.MethodPatch, path: "/api/model-evaluations/" + created.ID, body: map[string]any{"expected_version": created.Version}},
		{name: "delete store", method: http.MethodDelete, path: "/api/model-evaluations/" + created.ID, body: map[string]any{"expected_version": created.Version}},
		{name: "corpus store", method: http.MethodGet, path: "/api/model-evaluations/" + created.ID + "/corpus"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := repositoryModelEvaluationRequest(t, mux, test.method, test.path, test.body)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRepositoryModelEvaluationAPIActionsAliasAndErrorMappings(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)

	duplicateErr := validateRepositoryModelEvaluationAliases(
		mustLoadRepositoryModelEvaluationConfig(t, handler.configPath),
		[]string{"model-a", "model-a"},
		"selector",
		"judge",
	)
	if !errors.Is(duplicateErr, repoeval.ErrInvalidEvaluation) {
		t.Fatalf("duplicate alias error=%v", duplicateErr)
	}
	delimiterErr := validateRepositoryModelEvaluationAliases(
		mustLoadRepositoryModelEvaluationConfig(t, handler.configPath),
		[]string{"model,a", "model-b"},
		"selector",
		"judge",
	)
	if !errors.Is(delimiterErr, repoeval.ErrInvalidEvaluation) ||
		repositoryModelEvaluationAliasTransportSafe("model,a") ||
		!repositoryModelEvaluationAliasTransportSafe("model-a") {
		t.Fatalf("delimiter alias validation error=%v", delimiterErr)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/model-evaluations/id/action",
		strings.NewReader(`{"expected_version":1}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.SetPathValue("id", repositoryModelEvaluationMissingID())
	response := httptest.NewRecorder()
	handler.handleRepositoryModelEvaluationAction(response, request, "unknown")
	if response.Code != http.StatusConflict {
		t.Fatalf("unknown action status=%d body=%s", response.Code, response.Body.String())
	}

	nilHandlerRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/model-evaluations/id/action",
		strings.NewReader(`{"expected_version":1}`),
	)
	nilHandlerRequest.Header.Set("Content-Type", "application/json")
	nilHandlerRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	nilHandlerResponse := httptest.NewRecorder()
	var nilHandler *Handler
	nilHandler.handleRepositoryModelEvaluationAction(nilHandlerResponse, nilHandlerRequest, "unknown")
	if nilHandlerResponse.Code != http.StatusInternalServerError {
		t.Fatalf("nil handler status=%d body=%s", nilHandlerResponse.Code, nilHandlerResponse.Body.String())
	}

	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not found", err: os.ErrNotExist, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "controller locked", err: repoeval.ErrControllerLocked, wantStatus: http.StatusConflict, wantCode: "stale_repository_model_evaluation"},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, wantStatus: http.StatusBadRequest, wantCode: "invalid_repository_model_evaluation"},
		{name: "syntax", err: &json.SyntaxError{}, wantStatus: http.StatusBadRequest, wantCode: "invalid_repository_model_evaluation"},
		{name: "type mismatch", err: &json.UnmarshalTypeError{Value: "string", Type: reflect.TypeOf(0)}, wantStatus: http.StatusBadRequest, wantCode: "invalid_repository_model_evaluation"},
		{name: "unknown field", err: errors.New("unknown field example"), wantStatus: http.StatusBadRequest, wantCode: "invalid_repository_model_evaluation"},
		{name: "internal", err: errors.New("secret backend path"), wantStatus: http.StatusInternalServerError, wantCode: "repository_model_evaluation_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeRepositoryModelEvaluationError(response, test.err)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.wantStatus >= 500 && strings.Contains(response.Body.String(), "secret backend path") {
				t.Fatalf("internal error leaked: %s", response.Body.String())
			}
		})
	}
}

func TestRepositoryModelEvaluationOptionsProjectSortedSafeRepositories(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg := mustLoadRepositoryModelEvaluationConfig(t, handler.configPath)
	root := filepath.Join(t.TempDir(), "git-workspaces")
	cfg.GitWorkspaces.RootDir = root
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "checkouts"), 0o755); err != nil {
		t.Fatal(err)
	}

	remotes := []string{
		"https://github.com/zeta/repo-z.git",
		"https://github.com/acme/repo-a.git",
		"alice@git.example:group/incompatible.git",
		filepath.Join(t.TempDir(), "local-secret.git"),
	}
	repositories := make(map[string]any, len(remotes))
	workspaces := make(map[string]any, len(remotes))
	now := time.Now().UTC()
	for index, remote := range remotes {
		id := repositoryModelEvaluationGitRepositoryID(remote)
		workspaceID := fmt.Sprintf("workspace-%d", index)
		workspacePath := filepath.Join(root, "workspace-"+fmt.Sprint(index))
		if err := os.MkdirAll(workspacePath, 0o755); err != nil {
			t.Fatal(err)
		}
		repositories[id] = map[string]any{
			"id": id, "remote_url": remote, "first_seen_at": now, "last_seen_at": now,
			"workspace_ids": []string{workspaceID},
		}
		workspace := map[string]any{
			"id": workspaceID, "repo_id": id, "remote_url": remote, "path": workspacePath,
			"created_at": now, "updated_at": now,
		}
		if filepath.IsAbs(remote) {
			workspace["upstream_url"] = "https://github.com/local/upstream.git"
		}
		workspaces[workspaceID] = workspace
	}
	inventory, err := json.Marshal(map[string]any{
		"version": "4", "repositories": repositories, "workspaces": workspaces,
		"development_lines": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inventory.json"), inventory, 0o600); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-evaluations/options", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("options status=%d body=%s", response.Code, response.Body.String())
	}
	var options struct {
		Repositories []repositoryModelEvaluationRepositoryOption `json:"repositories"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Repositories) != 3 || options.Repositories[0].Label != "repo-a" ||
		options.Repositories[1].Label != "repo-z" ||
		options.Repositories[2].Repository != "https://github.com/local/upstream.git" {
		t.Fatalf("repositories=%#v", options.Repositories)
	}
}

func TestRepositoryModelEvaluationOptionsTolerateWorkspaceManagerFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*testing.T, *config.Config)
	}{
		{name: "manager construction", configure: func(t *testing.T, cfg *config.Config) {
			root := filepath.Join(t.TempDir(), "root-file")
			if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.GitWorkspaces.RootDir = root
		}},
		{name: "manager stats", configure: func(t *testing.T, cfg *config.Config) {
			root := filepath.Join(t.TempDir(), "root")
			if err := os.MkdirAll(filepath.Join(root, "checkouts"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "inventory.json"), []byte(`{"version":`), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.GitWorkspaces.RootDir = root
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
			t.Cleanup(handler.Shutdown)
			cfg := mustLoadRepositoryModelEvaluationConfig(t, handler.configPath)
			test.configure(t, cfg)
			if err := config.SaveConfig(handler.configPath, cfg); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-evaluations/options", nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"repositories":[]`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRepositoryModelEvaluationPageProjectionAndIdentityBoundaries(t *testing.T) {
	if _, _, err := repositoryModelEvaluationPage(nil); err == nil {
		t.Fatal("nil request page accepted")
	}
	nilURL := &http.Request{}
	if _, _, err := repositoryModelEvaluationPage(nilURL); err == nil {
		t.Fatal("nil URL page accepted")
	}

	repository := "/absolute/checkout/core"
	evaluation := repoeval.Evaluation{
		ID: repositoryModelEvaluationMissingID(), Repository: repository,
		Progress:   repoeval.Progress{Message: "failed in " + repository},
		Failure:    "could not inspect " + repository,
		Corpus:     &repoeval.CorpusManifest{Files: []repoeval.CorpusFile{{Path: "secret.go"}}},
		Checkpoint: repoeval.Checkpoint{ConcreteModels: map[string]map[string]int{"model-a": {"gpt-a": 1}}},
		RunIDs:     []string{"one", "two"},
	}
	summary := projectRepositoryModelEvaluationSummary(evaluation)
	projected := projectRepositoryModelEvaluation(evaluation)
	if summary.Repository != "" || strings.Contains(summary.Progress.Message, repository) ||
		strings.Contains(summary.Failure, repository) || len(projected.Corpus.Files) != 0 ||
		len(projected.Checkpoint.ConcreteModels) != 0 || len(projected.RunIDs) != 2 {
		t.Fatalf("summary=%#v projected=%#v", summary, projected)
	}

	for _, identity := range []string{
		"", "   ", "/absolute/repo", "file:///tmp/repo", "https://user@example.test/repo",
		"https://example.test/repo?token=secret", "https://example.test/repo#fragment",
	} {
		if sanitized := sanitizeRepositoryModelEvaluationIdentity(identity); sanitized != "" {
			t.Fatalf("identity %q sanitized to %q", identity, sanitized)
		}
	}
}

func repositoryModelEvaluationRequest(
	t *testing.T,
	mux *http.ServeMux,
	method, path string,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func mustLoadRepositoryModelEvaluationConfig(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func repositoryModelEvaluationMissingID() string {
	return "rme_" + strings.Repeat("a", 32)
}

func repositoryModelEvaluationGitRepositoryID(repository string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(repository))))
	return "gw-" + hex.EncodeToString(sum[:])[:12]
}

func newRepositoryModelEvaluationTestHandler(t *testing.T) (*Handler, *http.ServeMux, string) {
	t.Helper()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.AccountRef = "api"
	cfg.Agents.Defaults.ModelName = "model-a"
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "model-a", Model: "openai/gpt-a"},
		{Name: "model-b", Model: "openai/gpt-b"},
		{Name: "selector", Model: "openai/gpt-selector"},
		{Name: "judge", Model: "openai/gpt-judge"},
		{Name: "unsafe", Model: "codex-cli/codex"},
	}
	cfg.ModelList = []*config.ModelConfig{{ModelName: "api", Provider: "openai", Model: "openai/gpt-a", Enabled: true}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return handler, mux, workspace
}

func repositoryModelEvaluationCreateBody(repository string) map[string]any {
	return map[string]any{
		"repository": repository, "ref": "main", "candidate_models": []string{"model-a", "model-b"},
		"selector_model_alias": "selector", "judge_model_alias": "judge",
		"focus":                      map[string]any{"code_types": []string{"code", "test"}},
		"default_files_per_language": 20, "files_per_language": map[string]int{"go": 20},
	}
}

func createRepositoryModelEvaluation(t *testing.T, mux *http.ServeMux, repository string) repoeval.Evaluation {
	t.Helper()
	response := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations",
		repositoryModelEvaluationCreateBody(repository),
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var detail repositoryModelEvaluationDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	return detail.Evaluation
}

func repositoryModelEvaluationMutation(
	t *testing.T,
	mux *http.ServeMux,
	method, path string,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func waitRepositoryModelEvaluationStatus(
	t *testing.T,
	handler *Handler,
	id string,
	status repoeval.Status,
) repoeval.Evaluation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		evaluation, found, err := handler.getRepositoryModelEvaluation(t.Context(), id)
		if err == nil && found && evaluation.Status == status {
			return evaluation
		}
		time.Sleep(10 * time.Millisecond)
	}
	evaluation, _, err := handler.getRepositoryModelEvaluation(t.Context(), id)
	t.Fatalf("evaluation %s status=%s want=%s err=%v", id, evaluation.Status, status, err)
	return repoeval.Evaluation{}
}

func repositoryModelEvaluationPreflightResult() *workflows.RunResult {
	commit := strings.Repeat("a", 40)
	inventory := strings.Repeat("b", 64)
	candidates := []reposcope.Candidate{
		{
			ID:          "cand_" + strings.Repeat("c", 64),
			CommitID:    commit,
			InventoryID: inventory,
			Path:        "pkg/core.go",
			BlobID:      strings.Repeat("d", 40),
			Size:        9000,
			Language:    "go",
			CodeType:    reposcope.CodeTypeCode,
			Module:      "pkg",
			Region:      "pkg",
		},
		{
			ID:          "cand_" + strings.Repeat("e", 64),
			CommitID:    commit,
			InventoryID: inventory,
			Path:        "web/app.ts",
			BlobID:      strings.Repeat("f", 40),
			Size:        8000,
			Language:    "typescript",
			CodeType:    reposcope.CodeTypeCode,
			Module:      "web",
			Region:      "web",
		},
	}
	return &workflows.RunResult{Status: workflows.RunStatusSucceeded, Outputs: map[string]any{
		"commit":        commit,
		"inventoryHash": inventory,
		"catalog": map[string]any{
			"counts": map[string]any{
				"eligibleFiles":       2,
				"availableByLanguage": map[string]int{"go": 1, "typescript": 1},
			},
		},
		"selection": map[string]any{"selected": candidates},
		"selector":  map[string]any{"rationale": "Cross-language representative sample.", "warnings": []string{}},
	}}
}

func repositoryModelEvaluationBatchResult() *workflows.RunResult {
	return &workflows.RunResult{Status: workflows.RunStatusSucceeded, Outputs: map[string]any{
		"candidates": []map[string]any{
			{
				"model": map[string]any{"requested": "model-a", "selected": "gpt-a"},
				"valid": true,
				"scope": []map[string]any{{"path": "pkg/core.go"}, {"path": "web/app.ts"}},
			},
			{
				"model": map[string]any{"requested": "model-b", "selected": "gpt-b"},
				"valid": true,
				"scope": []map[string]any{{"path": "pkg/core.go"}, {"path": "web/app.ts"}},
			},
		},
		"mapping": []map[string]any{
			{"candidateId": "candidate-001", "modelAlias": "model-a"},
			{"candidateId": "candidate-002", "modelAlias": "model-b"},
		},
		"judge": map[string]any{"evaluations": []map[string]any{
			{"candidateId": "candidate-001", "confirmedClaims": 3, "unsupportedClaims": 0},
			{"candidateId": "candidate-002", "confirmedClaims": 2, "unsupportedClaims": 1},
		}},
	}}
}

func repositoryModelEvaluationAnalysisResult() *workflows.RunResult {
	scoreA, scoreB := 92.0, 84.0
	return &workflows.RunResult{Status: workflows.RunStatusSucceeded, Outputs: map[string]any{
		"comparison": map[string]any{"warnings": []string{"AI judged."}, "comparisons": []map[string]any{
			{
				"modelAlias": "model-a",
				"rank":       1,
				"completion": "completed",
				"scores": map[string]float64{
					"correctness":   95,
					"evidence":      91,
					"coverage":      90,
					"actionability": 92,
				},
				"overallScore": scoreA,
				"verdict":      "Best supported",
				"strengths":    []string{"Evidence"},
				"limitations":  []string{},
			},
			{
				"modelAlias": "model-b",
				"rank":       2,
				"completion": "completed",
				"scores": map[string]float64{
					"correctness":   85,
					"evidence":      84,
					"coverage":      83,
					"actionability": 84,
				},
				"overallScore": scoreB,
				"verdict":      "Good",
				"strengths":    []string{},
				"limitations":  []string{"One unsupported claim"},
			},
		}},
	}}
}
