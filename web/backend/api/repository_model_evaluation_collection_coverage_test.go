package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoeval"
)

type repositoryEvaluationFailingReadCloser struct{}

func (repositoryEvaluationFailingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("injected read failure")
}

func (repositoryEvaluationFailingReadCloser) Close() error { return nil }

func requireRepositoryModelEvaluationCoverageError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if response.Code != wantStatus || !strings.Contains(
		response.Body.String(),
		`"code":"`+wantCode+`"`,
	) {
		t.Fatalf(
			"status=%d body=%s, want status=%d code=%q",
			response.Code,
			response.Body.String(),
			wantStatus,
			wantCode,
		)
	}
}

func TestRepositoryModelEvaluationCollectionQueryFieldsPagingAndErrors(t *testing.T) {
	handler, mux, workspace := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	firstEvaluation := createRepositoryModelEvaluation(t, mux, "owner/query-one")
	secondEvaluation := createRepositoryModelEvaluation(t, mux, "owner/query-two")
	if !secondEvaluation.CreatedAt.After(firstEvaluation.CreatedAt) {
		t.Fatalf(
			"query fixtures need distinct creation times: first=%s second=%s",
			firstEvaluation.CreatedAt,
			secondEvaluation.CreatedAt,
		)
	}
	store := repoeval.NewStore(workspace)
	secondEvaluation, err := store.Update(
		t.Context(),
		secondEvaluation.ID,
		secondEvaluation.Version,
		func(value *repoeval.Evaluation) error {
			value.Ref = "feature/query-coverage"
			value.CandidateModels = append(value.CandidateModels, "judge")
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondEvaluation, err = store.Update(
		t.Context(),
		secondEvaluation.ID,
		secondEvaluation.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusPreflighting
			value.Progress.Stage = repoeval.ProgressResolving
			value.Progress.Percent = 25
			value.RunIDs = append(value.RunIDs, "run-query-coverage")
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	queries := []struct {
		field string
		query string
	}{
		{field: "id", query: `id = "` + firstEvaluation.ID + `"`},
		{field: "status", query: `status = draft`},
		{field: "repository", query: `repository = "` + firstEvaluation.Repository + `"`},
		{field: "ref", query: `ref = "main"`},
		{field: "models", query: `models = 2`},
		{field: "progress", query: `progress = 0`},
		{field: "version", query: `version = 1`},
		{
			field: "created",
			query: `created = "` + firstEvaluation.CreatedAt.Format(time.RFC3339Nano) + `"`,
		},
		{
			field: "updated",
			query: `updated = "` + firstEvaluation.UpdatedAt.Format(time.RFC3339Nano) + `"`,
		},
	}
	for _, test := range queries {
		t.Run(test.field, func(t *testing.T) {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet,
				"/api/model-evaluations?query="+url.QueryEscape(test.query),
				nil,
			))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var page struct {
				Evaluations    []repositoryModelEvaluationSummary `json:"evaluations"`
				Total          int                                `json:"total"`
				CanonicalQuery string                             `json:"canonical_query"`
				QuerySchema    json.RawMessage                    `json:"query_schema"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 || len(page.Evaluations) != 1 ||
				page.Evaluations[0].ID != firstEvaluation.ID || page.CanonicalQuery == "" ||
				!json.Valid(page.QuerySchema) {
				t.Fatalf("query %q page=%#v", test.query, page)
			}
		})
	}

	orderedQuery := `models >= 1 ORDER BY created ASC, repository DESC, id ASC`
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest(
		http.MethodGet,
		"/api/model-evaluations?query="+url.QueryEscape(orderedQuery)+"&limit=1",
		nil,
	))
	if first.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPage struct {
		Evaluations []repositoryModelEvaluationSummary `json:"evaluations"`
		Total       int                                `json:"total"`
		NextCursor  string                             `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if firstPage.Total != 2 || len(firstPage.Evaluations) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("first page=%#v", firstPage)
	}
	second := httptest.NewRecorder()
	mux.ServeHTTP(second, httptest.NewRequest(
		http.MethodGet,
		"/api/model-evaluations?query="+url.QueryEscape(orderedQuery)+"&limit=1&cursor="+
			url.QueryEscape(firstPage.NextCursor),
		nil,
	))
	if second.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	var secondPage struct {
		Evaluations []repositoryModelEvaluationSummary `json:"evaluations"`
		Total       int                                `json:"total"`
		NextCursor  string                             `json:"next_cursor"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if firstPage.Evaluations[0].ID != firstEvaluation.ID || secondPage.Total != 2 ||
		len(secondPage.Evaluations) != 1 || secondPage.Evaluations[0].ID != secondEvaluation.ID ||
		secondPage.Evaluations[0].ID == firstPage.Evaluations[0].ID || secondPage.NextCursor != "" {
		t.Fatalf("first page=%#v second page=%#v", firstPage, secondPage)
	}
	mismatch := httptest.NewRecorder()
	mux.ServeHTTP(mismatch, httptest.NewRequest(
		http.MethodGet,
		"/api/model-evaluations?query="+url.QueryEscape(`id = "`+secondEvaluation.ID+`"`)+
			"&limit=1&cursor="+url.QueryEscape(firstPage.NextCursor),
		nil,
	))
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("cursor mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	invalidQuery := httptest.NewRecorder()
	mux.ServeHTTP(invalidQuery, httptest.NewRequest(
		http.MethodGet,
		"/api/model-evaluations?query=unknown%20%3D%201",
		nil,
	))
	if invalidQuery.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status=%d body=%s", invalidQuery.Code, invalidQuery.Body.String())
	}

	if !validRepositoryModelEvaluationCollectionID(firstEvaluation.ID) ||
		validRepositoryModelEvaluationCollectionID("invalid") ||
		validRepositoryModelEvaluationCollectionID("rme_"+strings.Repeat("A", 32)) {
		t.Fatal("repository evaluation collection ID validation accepted an invalid boundary")
	}

	t.Run("configuration load failure", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte("{not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		bad := NewHandler(configPath)
		badMux := http.NewServeMux()
		bad.RegisterRoutes(badMux)
		response := httptest.NewRecorder()
		badMux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-evaluations", nil))
		requireRepositoryModelEvaluationCoverageError(
			t, response, http.StatusBadRequest, "invalid_repository_model_evaluation",
		)
	})

	t.Run("unsafe store root", func(t *testing.T) {
		root := filepath.Join(workspace, "repository_evaluations")
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-evaluations", nil))
		requireRepositoryModelEvaluationCoverageError(
			t, response, http.StatusInternalServerError, "repository_model_evaluation_unavailable",
		)
	})
}

func TestRepositoryModelEvaluationBulkDeleteNoopAndErrorBoundaries(t *testing.T) {
	t.Run("mixed retained versions", func(t *testing.T) {
		handler, mux, workspace := newRepositoryModelEvaluationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		active := createRepositoryModelEvaluation(t, mux, "owner/active")
		stale := createRepositoryModelEvaluation(t, mux, "owner/stale-retained")
		store := repoeval.NewStore(workspace)
		active, err := store.Update(t.Context(), active.ID, active.Version, func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusPreflighting
			value.RunIDs = append(value.RunIDs, "run-active")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		response := repositoryModelEvaluationMutation(
			t,
			mux,
			http.MethodPost,
			"/api/model-evaluations/bulk-delete",
			map[string]any{"items": []map[string]any{
				{"id": active.ID, "version": active.Version},
				{"id": stale.ID, "version": stale.Version + 1},
				{"id": "rme_" + strings.Repeat("f", 32), "version": 1},
				{"id": "invalid", "version": 1},
				{"id": "rme_" + strings.Repeat("e", 32), "version": 0},
			}},
		)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		var result repoeval.BulkDeleteResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		codes := make(map[string]string, len(result.Failures))
		for _, failure := range result.Failures {
			codes[failure.ID] = failure.Code
		}
		if len(result.DeletedIDs) != 0 || len(result.Failures) != 5 ||
			codes[active.ID] != "not_draft" || codes[stale.ID] != "stale_version" ||
			codes["invalid"] != "invalid_id" ||
			codes["rme_"+strings.Repeat("f", 32)] != "not_found" ||
			codes["rme_"+strings.Repeat("e", 32)] != "invalid_version" {
			t.Fatalf("result=%#v", result)
		}
	})

	t.Run("mutation guard", func(t *testing.T) {
		handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/model-evaluations/bulk-delete?unexpected=1",
			strings.NewReader(`{"items":[{"id":"invalid","version":1}]}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("configuration load failure", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte("{not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		handler := NewHandler(configPath)
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/model-evaluations/bulk-delete",
			strings.NewReader(`{"items":[{"id":"rme_ffffffffffffffffffffffffffffffff","version":1}]}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		requireRepositoryModelEvaluationCoverageError(
			t, response, http.StatusBadRequest, "invalid_repository_model_evaluation",
		)
	})

	t.Run("store cancellation", func(t *testing.T) {
		handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		evaluation := createRepositoryModelEvaluation(t, mux, "owner/canceled")
		body, err := json.Marshal(map[string]any{"items": []map[string]any{{
			"id": evaluation.ID, "version": evaluation.Version,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/model-evaluations/bulk-delete",
			bytes.NewReader(body),
		).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		requireRepositoryModelEvaluationCoverageError(
			t, response, http.StatusInternalServerError, "repository_model_evaluation_unavailable",
		)
	})
}

func TestRepositoryModelEvaluationDecoderRemainingBoundaries(t *testing.T) {
	decode := func(request *http.Request) error {
		t.Helper()
		var destination repositoryModelEvaluationBulkDeleteRequest
		return decodeRepositoryModelEvaluationRequest(request, &destination)
	}

	wrongMediaType := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	wrongMediaType.Header.Set("Content-Type", "text/plain")
	if err := decode(wrongMediaType); !errors.Is(err, errRepositoryModelEvaluationMediaType) {
		t.Fatalf("wrong media type error=%v", err)
	}
	badParameter := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	badParameter.Header.Set("Content-Type", "application/json; profile=custom")
	if err := decode(badParameter); !errors.Is(err, errRepositoryModelEvaluationMediaType) {
		t.Fatalf("unsupported media parameter error=%v", err)
	}

	readFailure := &http.Request{
		Method: http.MethodPost, URL: &url.URL{}, Header: make(http.Header),
		Body: repositoryEvaluationFailingReadCloser{}, ContentLength: -1,
	}
	readFailure.Header.Set("Content-Type", "application/json")
	if err := decode(readFailure); err == nil || errors.Is(err, errRepositoryModelEvaluationMediaType) {
		t.Fatalf("body read failure error=%v", err)
	}

	unknownLengthOversized := &http.Request{
		Method: http.MethodPost, URL: &url.URL{}, Header: make(http.Header), ContentLength: -1,
		Body: io.NopCloser(strings.NewReader(strings.Repeat(" ", repositoryModelEvaluationRequestMaxBytes+1))),
	}
	unknownLengthOversized.Header.Set("Content-Type", "application/json")
	if err := decode(unknownLengthOversized); !errors.Is(err, errRepositoryModelEvaluationRequestTooLarge) {
		t.Fatalf("unknown-length oversized error=%v", err)
	}

	trailing := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{} {}`))
	trailing.Header.Set("Content-Type", "application/json")
	if err := decode(trailing); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}
