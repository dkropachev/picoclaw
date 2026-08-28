package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
)

const testPRWorkspaceID = "devw_11111111111111111111111111111111"

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

func TestPRWorkspaceCollectionProxyAllowsBoundedPercentEncodedQuery(t *testing.T) {
	called := false
	var capturedQuery string
	installEventProxyStubs(t, func(request *http.Request, _ time.Duration) (*http.Response, error) {
		called = true
		capturedQuery = request.URL.RawQuery
		return eventUpstreamResponse(http.StatusOK, `{"workspaces":[],"total":0}`), nil
	})
	handler := NewHandler(t.TempDir() + "/config.json")

	query := `title ~ "` + strings.Repeat("é", 1800) + `"`
	rawQuery := url.Values{"query": {query}, "limit": {"200"}}.Encode()
	if len(query) > collectionquery.MaxQueryBytes || len(rawQuery) <= prWorkspaceMaxQueryBytes ||
		len(rawQuery) > prWorkspaceMaxListQueryBytes {
		t.Fatalf("test query bounds decoded=%d encoded=%d", len(query), len(rawQuery))
	}
	response := httptest.NewRecorder()
	handler.handlePRWorkspaceProxy(response, httptest.NewRequest(
		http.MethodGet,
		"http://launcher.local"+prWorkspaceAPIPath+"?"+rawQuery,
		nil,
	))
	if response.Code != http.StatusOK || !called || capturedQuery != rawQuery {
		t.Fatalf(
			"collection proxy status=%d called=%t query_match=%t body=%s",
			response.Code,
			called,
			capturedQuery == rawQuery,
			response.Body.String(),
		)
	}

	for _, request := range []*http.Request{
		httptest.NewRequest(
			http.MethodGet,
			"http://launcher.local"+prWorkspaceAPIPath+"?query="+
				strings.Repeat("x", prWorkspaceMaxListQueryBytes),
			nil,
		),
		httptest.NewRequest(
			http.MethodGet,
			"http://launcher.local"+prWorkspaceAPIPath+"/"+testPRWorkspaceID+"?query="+
				strings.Repeat("x", prWorkspaceMaxQueryBytes),
			nil,
		),
	} {
		called = false
		response = httptest.NewRecorder()
		handler.handlePRWorkspaceProxy(response, request)
		if response.Code != http.StatusBadRequest || called {
			t.Fatalf("oversized query status=%d called=%t", response.Code, called)
		}
	}
}

func TestPRWorkspaceDirectQueryPassesThroughRuntimeRejection(t *testing.T) {
	var capturedPath, capturedQuery string
	installEventProxyStubs(t, func(request *http.Request, _ time.Duration) (*http.Response, error) {
		capturedPath = request.URL.Path
		capturedQuery = request.URL.RawQuery
		return eventUpstreamResponse(
			http.StatusBadRequest,
			`{"code":"invalid_query","message":"invalid query"}`,
		), nil
	})
	handler := NewHandler(t.TempDir() + "/config.json")
	response := httptest.NewRecorder()
	handler.handlePRWorkspaceProxy(response, httptest.NewRequest(
		http.MethodGet,
		"http://launcher.local"+prWorkspaceAPIPath+"/"+testPRWorkspaceID+"?anything=value",
		nil,
	))
	if response.Code != http.StatusBadRequest ||
		capturedPath != prWorkspaceRuntimePath+"/"+testPRWorkspaceID ||
		capturedQuery != "anything=value" ||
		!strings.Contains(response.Body.String(), `"code":"invalid_query"`) {
		t.Fatalf(
			"direct query status=%d path=%q query=%q body=%s",
			response.Code,
			capturedPath,
			capturedQuery,
			response.Body.String(),
		)
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
		prWorkspaceRuntimePath + "/devw_1111111111111111111111111111111g",
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
		"/api//development-workspaces",
		"/api/ignored/../development-workspaces",
		prWorkspaceAPIPath + "/./" + testPRWorkspaceID,
		prWorkspaceAPIPath + "/" + testPRWorkspaceID + "/../../status",
		"/api//notifications",
		"/api/ignored/../notification-views",
		"/api/development/./repositories",
		"/api/development/ignored/../workflow-configurations",
	} {
		called = false
		response := httptest.NewRecorder()
		guarded.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if response.Code != http.StatusBadRequest || called {
			t.Fatalf("alias %q status = %d, called=%v", requestPath, response.Code, called)
		}
	}
}

func TestDevelopmentNotificationRoutesProxyReadAndMutationContracts(t *testing.T) {
	type capturedRequest struct {
		method   string
		path     string
		query    string
		body     string
		timeout  time.Duration
		bearer   string
		mimeType string
	}
	var captured []capturedRequest
	installEventProxyStubs(t, func(request *http.Request, timeout time.Duration) (*http.Response, error) {
		var body []byte
		if request.Body != nil {
			body, _ = io.ReadAll(request.Body)
		}
		captured = append(captured, capturedRequest{
			method: request.Method, path: request.URL.Path, query: request.URL.RawQuery,
			body: string(body), timeout: timeout, bearer: request.Header.Get("Authorization"),
			mimeType: request.Header.Get("Content-Type"),
		})
		return eventUpstreamResponse(http.StatusOK, `{"ok":true}`), nil
	})

	handler := NewHandler(t.TempDir() + "/config.json")
	mux := http.NewServeMux()
	handler.registerPRWorkspaceRoutes(mux)
	tests := []struct {
		method string
		path   string
		body   string
		want   string
	}{
		{http.MethodGet, "/api/notifications?query=status%3Aopen", "", prWorkspaceRuntimePath + "/notifications"},
		{http.MethodGet, "/api/notification-views", "", prWorkspaceRuntimePath + "/notification-views"},
		{http.MethodGet, "/api/notification-settings", "", prWorkspaceRuntimePath + "/notification-settings"},
		{
			http.MethodGet,
			"/api/push-subscriptions/device-1",
			"",
			prWorkspaceRuntimePath + "/push-subscriptions/device-1",
		},
		{
			http.MethodPost,
			"/api/notifications/bulk",
			`{"request_id":"request-notification-bulk","action":"mark_read","items":[]}`,
			prWorkspaceRuntimePath + "/notifications/bulk",
		},
		{
			http.MethodPut,
			"/api/notification-views",
			`{"request_id":"request-notification-views"}`,
			prWorkspaceRuntimePath + "/notification-views",
		},
		{
			http.MethodDelete,
			"/api/push-subscriptions/device-1",
			`{"request_id":"request-notification-delete"}`,
			prWorkspaceRuntimePath + "/push-subscriptions/device-1",
		},
	}
	for index, test := range tests {
		var body io.Reader
		if test.body != "" {
			body = strings.NewReader(test.body)
		}
		request := httptest.NewRequest(test.method, "http://launcher.local"+test.path, body)
		if test.method != http.MethodGet {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
			t.Fatalf("%s %s response = %d %s", test.method, test.path, response.Code, response.Body.String())
		}
		got := captured[index]
		wantTimeout := prWorkspaceReadTimeout
		if test.method != http.MethodGet {
			wantTimeout = time.Minute
		}
		if got.method != test.method || got.path != test.want || got.timeout != wantTimeout ||
			got.bearer != "Bearer gateway-pid-token" || got.body != test.body {
			t.Fatalf("captured %s %s = %#v", test.method, test.path, got)
		}
		if test.method != http.MethodGet && got.mimeType != "application/json" {
			t.Fatalf("mutation content type = %q", got.mimeType)
		}
	}
	if captured[0].query != "query=status%3Aopen" {
		t.Fatalf("notification query = %q", captured[0].query)
	}
}

func TestDevelopmentNotificationProxyRejectsUnsafeTransportShapes(t *testing.T) {
	called := false
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		called = true
		return eventUpstreamResponse(http.StatusOK, `{}`), nil
	})
	handler := NewHandler(t.TempDir() + "/config.json")

	response := httptest.NewRecorder()
	handler.handleDevelopmentNotificationProxy(response, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("nil request status = %d", response.Code)
	}

	tests := []struct {
		name   string
		build  func() *http.Request
		status int
	}{
		{
			name: "unknown resource", status: http.StatusBadRequest,
			build: func() *http.Request { return httptest.NewRequest(http.MethodGet, "/api/not-notifications", nil) },
		},
		{
			name: "method", status: http.StatusMethodNotAllowed,
			build: func() *http.Request { return httptest.NewRequest(http.MethodPatch, "/api/notifications", nil) },
		},
		{
			name: "GET body", status: http.StatusBadRequest,
			build: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/notifications", strings.NewReader("unexpected"))
			},
		},
		{
			name: "mutation query", status: http.StatusBadRequest,
			build: func() *http.Request {
				request := httptest.NewRequest(
					http.MethodPost,
					"/api/notifications/bulk?unsafe=1",
					strings.NewReader(`{}`),
				)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Sec-Fetch-Site", "same-origin")
				return request
			},
		},
		{
			name: "cross site", status: http.StatusBadRequest,
			build: func() *http.Request {
				request := httptest.NewRequest(http.MethodPost, "/api/notifications/bulk", strings.NewReader(`{}`))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Sec-Fetch-Site", "cross-site")
				return request
			},
		},
		{
			name: "invalid content type", status: http.StatusBadRequest,
			build: func() *http.Request {
				request := httptest.NewRequest(http.MethodPut, "/api/notification-views", strings.NewReader(`{}`))
				request.Header.Set("Content-Type", "text/plain")
				request.Header.Set("Sec-Fetch-Site", "same-origin")
				return request
			},
		},
		{
			name: "empty body", status: http.StatusBadRequest,
			build: func() *http.Request {
				request := httptest.NewRequest(http.MethodDelete, "/api/push-subscriptions/device", http.NoBody)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Sec-Fetch-Site", "same-origin")
				return request
			},
		},
		{
			name: "oversized body", status: http.StatusBadRequest,
			build: func() *http.Request {
				request := httptest.NewRequest(
					http.MethodPost,
					"/api/push-subscriptions",
					strings.NewReader(strings.Repeat("x", prWorkspaceMaxBodyBytes+1)),
				)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Sec-Fetch-Site", "same-origin")
				return request
			},
		},
		{
			name: "forced query", status: http.StatusBadRequest,
			build: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
				request.URL.ForceQuery = true
				return request
			},
		},
		{
			name: "fragment", status: http.StatusBadRequest,
			build: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
				request.URL.Fragment = "fragment"
				return request
			},
		},
		{
			name: "oversized query", status: http.StatusBadRequest,
			build: func() *http.Request {
				return httptest.NewRequest(
					http.MethodGet,
					"/api/notifications?q="+strings.Repeat("x", prWorkspaceMaxQueryBytes),
					nil,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called = false
			response := httptest.NewRecorder()
			handler.handleDevelopmentNotificationProxy(response, test.build())
			if response.Code != test.status || called {
				t.Fatalf("response = %d %s, called=%v", response.Code, response.Body.String(), called)
			}
			if test.name == "method" && response.Header().Get("Allow") != "GET, POST, PUT, DELETE" {
				t.Fatalf("Allow = %q", response.Header().Get("Allow"))
			}
		})
	}
}

func TestDevelopmentNotificationEventStreamStartsAndHonorsCancellation(t *testing.T) {
	handler := NewHandler(t.TempDir() + "/config.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/notifications/events/stream", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.handleDevelopmentNotificationProxy(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(response.Body.String(), "event: notification") {
		t.Fatalf("stream response = %d %#v %q", response.Code, response.Header(), response.Body.String())
	}
}
