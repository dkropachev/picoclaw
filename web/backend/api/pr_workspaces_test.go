package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testPRWorkspaceID = "prw_11111111111111111111111111111111"

func TestPRWorkspaceRoutesProxyUnifiedContract(t *testing.T) {
	var captured *http.Request
	var capturedBody string
	var capturedTimeout time.Duration
	installEventProxyStubs(t, func(request *http.Request, timeout time.Duration) (*http.Response, error) {
		captured = request
		capturedTimeout = timeout
		body, _ := io.ReadAll(request.Body)
		capturedBody = string(body)
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header: http.Header{
				"Content-Type": {"application/json"},
				"Location":     {prWorkspaceRuntimePath + "/" + testPRWorkspaceID},
			},
			Body:          io.NopCloser(strings.NewReader(`{"workspace":{"id":"` + testPRWorkspaceID + `"}}`)),
			ContentLength: int64(len(`{"workspace":{"id":"` + testPRWorkspaceID + `"}}`)),
		}, nil
	})

	handler := NewHandler(t.TempDir() + "/config.json")
	mux := http.NewServeMux()
	handler.registerPRWorkspaceRoutes(mux)
	request := httptest.NewRequest(
		http.MethodPost,
		"http://launcher.local"+prWorkspaceAPIPath+"/intake",
		strings.NewReader(`{"repository":"acme/widgets","pull_number":7}`),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != prWorkspaceAPIPath+"/"+testPRWorkspaceID {
		t.Fatalf("Location = %q", got)
	}
	if captured == nil || captured.Method != http.MethodPost ||
		captured.URL.Path != prWorkspaceRuntimePath+"/intake" ||
		captured.URL.RawQuery != "" ||
		captured.Header.Get("Authorization") != "Bearer gateway-pid-token" ||
		captured.Header.Get("Content-Type") != "application/json" ||
		capturedBody != `{"repository":"acme/widgets","pull_number":7}` ||
		capturedTimeout != prWorkspaceAIWriteTimeout {
		t.Fatalf("captured request = %#v, body=%q, timeout=%v", captured, capturedBody, capturedTimeout)
	}
}

func TestPRWorkspaceRoutesRejectCrossSiteAndLegacyPaths(t *testing.T) {
	called := false
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		called = true
		return nil, nil
	})

	handler := NewHandler(t.TempDir() + "/config.json")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	request := httptest.NewRequest(
		http.MethodPatch,
		"http://launcher.local"+prWorkspaceAPIPath+"/"+testPRWorkspaceID+"/charter",
		strings.NewReader(`{"expected_version":1}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("cross-site status = %d, called=%v, body=%s", response.Code, called, response.Body.String())
	}

	for _, path := range []string{"/api/reviews", "/api/reviews/anything", "/api/pr-development", "/api/pr-development/anything"} {
		response = httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy path %q status = %d", path, response.Code)
		}
	}
}

func TestExternalPRWorkspaceLocationRejectsNonCanonicalValues(t *testing.T) {
	if got, ok := externalPRWorkspaceLocation(prWorkspaceRuntimePath + "/" + testPRWorkspaceID); !ok ||
		got != prWorkspaceAPIPath+"/"+testPRWorkspaceID {
		t.Fatalf("valid location = %q, %v", got, ok)
	}
	for _, raw := range []string{
		prWorkspaceRuntimePath + "/PRW_11111111111111111111111111111111",
		prWorkspaceRuntimePath + "/" + testPRWorkspaceID + "/review",
		prWorkspaceRuntimePath + "/prw_1111111111111111111111111111111g",
		prWorkspaceRuntimePath + "/%70rw_11111111111111111111111111111111",
	} {
		if got, ok := externalPRWorkspaceLocation(raw); ok || got != "" {
			t.Fatalf("invalid location %q mapped to %q, %v", raw, got, ok)
		}
	}
}

func TestPRWorkspaceCanonicalPathGuardRejectsServeMuxAliases(t *testing.T) {
	called := false
	guarded := GuardPRWorkspaceCanonicalPaths(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, requestPath := range []string{
		"/api//pr-workspaces",
		"/api/ignored/../pr-workspaces",
		prWorkspaceAPIPath + "/./" + testPRWorkspaceID,
		prWorkspaceAPIPath + "/" + testPRWorkspaceID + "/../../status",
	} {
		called = false
		response := httptest.NewRecorder()
		guarded.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if response.Code != http.StatusBadRequest || called {
			t.Fatalf("alias %q status = %d, called=%v", requestPath, response.Code, called)
		}
	}
}
