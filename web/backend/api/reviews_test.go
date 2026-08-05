package api

import (
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

	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const (
	testReviewCaseID    = "prc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testReviewFindingID = "prf_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestReviewRoutesProxyExactContractWithPrivateBearer(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	type capturedRequest struct {
		method        string
		path          string
		query         url.Values
		body          string
		timeout       time.Duration
		authorization string
		contentType   string
		cookie        string
		origin        string
		fetchSite     string
		browserHeader string
	}
	var captured []capturedRequest
	installEventProxyStubs(
		t,
		func(request *http.Request, timeout time.Duration) (*http.Response, error) {
			var body []byte
			var err error
			if request.Body != nil {
				body, err = io.ReadAll(request.Body)
				if err != nil {
					t.Fatalf("read upstream body: %v", err)
				}
			}
			captured = append(captured, capturedRequest{
				method:        request.Method,
				path:          request.URL.Path,
				query:         request.URL.Query(),
				body:          string(body),
				timeout:       timeout,
				authorization: request.Header.Get("Authorization"),
				contentType:   request.Header.Get("Content-Type"),
				cookie:        request.Header.Get("Cookie"),
				origin:        request.Header.Get("Origin"),
				fetchSite:     request.Header.Get("Sec-Fetch-Site"),
				browserHeader: request.Header.Get("X-Browser-Only"),
			})
			response := eventUpstreamResponse(
				http.StatusOK,
				`{"ok":true,"large":9007199254740993}`,
			)
			response.Header.Set("Set-Cookie", "gateway-secret=cookie")
			response.Header.Set("WWW-Authenticate", "Bearer gateway-secret")
			response.Header.Set("X-Gateway-Secret", "secret")
			return response, nil
		},
	)

	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	tests := []struct {
		name         string
		method       string
		path         string
		body         string
		upstreamPath string
		upstream     url.Values
		timeout      time.Duration
	}{
		{
			name:   "list",
			method: http.MethodGet,
			path: "/api/reviews?status=open&repository=scylladb%2Fscylladb" +
				"&limit=25&cursor=v1_token",
			upstreamPath: "/runtime/eventing/reviews",
			upstream: url.Values{
				"status":     {"open"},
				"repository": {"scylladb/scylladb"},
				"limit":      {"25"},
				"cursor":     {"v1_token"},
			},
			timeout: reviewGatewayRequestTimeout,
		},
		{
			name:         "get",
			method:       http.MethodGet,
			path:         "/api/reviews/" + testReviewCaseID,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID,
			timeout:      reviewGatewayRequestTimeout,
		},
		{
			name:         "get attention",
			method:       http.MethodGet,
			path:         "/api/reviews/" + testReviewCaseID + "/attention",
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID + "/attention",
			timeout:      reviewGatewayRequestTimeout,
		},
		{
			name:   "respond to attention",
			method: http.MethodPost,
			path:   "/api/reviews/" + testReviewCaseID + "/attention/respond",
			body: `{"expected_case_version":12,"response_token":"sha256:` +
				strings.Repeat("a", 64) + `","response":"retain v1"}`,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID +
				"/attention/respond",
			timeout: reviewGatewayAIRequestTimeout,
		},
		{
			name:   "edit finding",
			method: http.MethodPatch,
			path: "/api/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID,
			body: `{"expected_version":2,"finding":{"title":"clearer"}}`,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID,
			timeout: reviewGatewayRequestTimeout,
		},
		{
			name:   "drop finding",
			method: http.MethodPost,
			path: "/api/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID + "/drop",
			body: `{"expected_version":3,"reason":"not actionable"}`,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID + "/drop",
			timeout: reviewGatewayRequestTimeout,
		},
		{
			name:   "restore finding",
			method: http.MethodPost,
			path: "/api/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID + "/restore",
			body: `{"expected_version":4}`,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID + "/restore",
			timeout: reviewGatewayRequestTimeout,
		},
		{
			name:         "chat",
			method:       http.MethodPost,
			path:         "/api/reviews/" + testReviewCaseID + "/chat",
			body:         `{"expected_version":5,"content":"why?"}`,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID + "/chat",
			timeout:      reviewGatewayAIRequestTimeout,
		},
		{
			name:   "rephrase",
			method: http.MethodPost,
			path: "/api/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID + "/rephrase",
			body: `{"expected_version":6,"instruction":"make concise"}`,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID + "/rephrase",
			timeout: reviewGatewayAIRequestTimeout,
		},
		{
			name:         "submit",
			method:       http.MethodPost,
			path:         "/api/reviews/" + testReviewCaseID + "/submit",
			body:         `{"expected_version":7}`,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID + "/submit",
			timeout:      reviewGatewayRequestTimeout,
		},
		{
			name:         "reconcile",
			method:       http.MethodPost,
			path:         "/api/reviews/" + testReviewCaseID + "/reconcile",
			body:         `{"expected_version":8,"resolution":"absent"}`,
			upstreamPath: "/runtime/eventing/reviews/" + testReviewCaseID + "/reconcile",
			timeout:      reviewGatewayRequestTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}
			request := httptest.NewRequest(test.method, test.path, body)
			request.Header.Set("Authorization", "Bearer browser-token")
			request.Header.Set("Cookie", "launcher=browser-secret")
			request.Header.Set("Origin", "http://example.com")
			request.Header.Set("Sec-Fetch-Site", "same-origin")
			request.Header.Set("X-Browser-Only", "do-not-forward")
			if test.body != "" {
				request.Header.Set(
					"Content-Type",
					"application/json; charset=utf-8",
				)
			}

			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf(
					"status = %d, body=%s",
					recorder.Code,
					recorder.Body.String(),
				)
			}
			if got := recorder.Body.String(); got !=
				`{"ok":true,"large":9007199254740993}` {
				t.Fatalf("body = %q", got)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("response was not marked no-store")
			}
			if recorder.Header().Get("Content-Type") != "application/json" {
				t.Fatalf(
					"Content-Type = %q",
					recorder.Header().Get("Content-Type"),
				)
			}
			for _, name := range []string{
				"Set-Cookie",
				"WWW-Authenticate",
				"X-Gateway-Secret",
			} {
				if got := recorder.Header().Get(name); got != "" {
					t.Fatalf("leaked response header %s=%q", name, got)
				}
			}

			got := captured[len(captured)-1]
			if got.method != test.method || got.path != test.upstreamPath {
				t.Fatalf(
					"upstream = %s %s, want %s %s",
					got.method,
					got.path,
					test.method,
					test.upstreamPath,
				)
			}
			if got.query.Encode() != test.upstream.Encode() {
				t.Fatalf(
					"upstream query = %q, want %q",
					got.query.Encode(),
					test.upstream.Encode(),
				)
			}
			if got.body != test.body {
				t.Fatalf("upstream body = %q, want %q", got.body, test.body)
			}
			if got.timeout != test.timeout {
				t.Fatalf(
					"timeout = %s, want %s",
					got.timeout,
					test.timeout,
				)
			}
			if got.authorization != "Bearer gateway-pid-token" {
				t.Fatalf("upstream authorization = %q", got.authorization)
			}
			if test.body != "" && got.contentType != "application/json" {
				t.Fatalf("upstream Content-Type = %q", got.contentType)
			}
			if got.cookie != "" ||
				got.origin != "" ||
				got.fetchSite != "" ||
				got.browserHeader != "" {
				t.Fatalf("browser headers reached gateway: %#v", got)
			}
			if strings.Contains(recorder.Body.String(), "gateway-pid-token") {
				t.Fatal("PID bearer leaked in response")
			}
		})
	}

	if len(captured) != len(tests) {
		t.Fatalf("upstream calls = %d, want %d", len(captured), len(tests))
	}
}

func TestReviewRoutesRejectNoncanonicalPathsQueriesAndMethods(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	upstreamCalls := 0
	installEventProxyStubs(
		t,
		func(*http.Request, time.Duration) (*http.Response, error) {
			upstreamCalls++
			return eventUpstreamResponse(http.StatusOK, `{"ok":true}`), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	tooLongCursor := strings.Repeat("a", reviewProxyCursorMaxBytes+1)
	badRequests := []string{
		"/api/reviews/",
		"/api/reviews/not-a-case",
		"/api/reviews/prc_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"/api/reviews/%70rc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"/api/reviews/" + testReviewCaseID + "?detail=true",
		"/api/reviews/" + testReviewCaseID + "/attention?run_id=private",
		"/api/reviews?unknown=value",
		"/api/reviews?status=",
		"/api/reviews?status=OPEN",
		"/api/reviews?status=open&status=stale",
		"/api/reviews?repository=",
		"/api/reviews?repository=scylladb",
		"/api/reviews?repository=scylla%2Fdb%2Fextra",
		"/api/reviews?repository=%20scylla%2Fdb",
		"/api/reviews?limit=0",
		"/api/reviews?limit=01",
		"/api/reviews?limit=101",
		"/api/reviews?cursor=",
		"/api/reviews?cursor=" + tooLongCursor,
		"/api/reviews?cursor=a;b",
	}
	for _, path := range badRequests {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, path, nil),
		)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf(
				"%s status = %d, want 400, body=%s",
				path,
				recorder.Code,
				recorder.Body.String(),
			)
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s was not marked no-store", path)
		}
	}

	notFound := []string{
		"/api/reviews/" + testReviewCaseID + "/unknown",
		"/api/reviews/" + testReviewCaseID + "/findings/" +
			testReviewFindingID + "/unknown",
		"/api/reviews/" + testReviewCaseID + "/findings/not-a-finding/drop",
	}
	for _, path := range notFound {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, path, nil),
		)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf(
				"%s status = %d, want 404, body=%s",
				path,
				recorder.Code,
				recorder.Body.String(),
			)
		}
	}

	wrongMethods := []struct {
		method string
		path   string
		allow  string
	}{
		{http.MethodPost, "/api/reviews", http.MethodGet},
		{http.MethodPost, "/api/reviews/" + testReviewCaseID, http.MethodGet},
		{
			http.MethodPost,
			"/api/reviews/" + testReviewCaseID + "/attention",
			http.MethodGet,
		},
		{
			http.MethodGet,
			"/api/reviews/" + testReviewCaseID + "/attention/respond",
			http.MethodPost,
		},
		{
			http.MethodPost,
			"/api/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID,
			http.MethodPatch,
		},
		{
			http.MethodPatch,
			"/api/reviews/" + testReviewCaseID + "/chat",
			http.MethodPost,
		},
		{
			http.MethodPatch,
			"/api/reviews/" + testReviewCaseID + "/reconcile",
			http.MethodPost,
		},
	}
	for _, test := range wrongMethods {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(
			recorder,
			httptest.NewRequest(test.method, test.path, nil),
		)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf(
				"%s %s status = %d, want 405",
				test.method,
				test.path,
				recorder.Code,
			)
		}
		if recorder.Header().Get("Allow") != test.allow {
			t.Fatalf(
				"%s %s Allow = %q, want %q",
				test.method,
				test.path,
				recorder.Header().Get("Allow"),
				test.allow,
			)
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid requests made %d upstream calls", upstreamCalls)
	}
}

func TestReviewProxyRejectsNonlocalOrIncompleteGatewayPIDData(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	tests := []struct {
		name   string
		mutate func(*ppid.PidFileData)
	}{
		{
			name: "hostname",
			mutate: func(pidData *ppid.PidFileData) {
				pidData.Host = "example.com"
			},
		},
		{
			name: "remote numeric address",
			mutate: func(pidData *ppid.PidFileData) {
				pidData.Host = "8.8.8.8"
			},
		},
		{
			name: "IPv4 wildcard",
			mutate: func(pidData *ppid.PidFileData) {
				pidData.Host = "0.0.0.0"
			},
		},
		{
			name: "IPv6 wildcard",
			mutate: func(pidData *ppid.PidFileData) {
				pidData.Host = "::"
			},
		},
		{
			name: "missing host",
			mutate: func(pidData *ppid.PidFileData) {
				pidData.Host = ""
			},
		},
		{
			name: "missing port",
			mutate: func(pidData *ppid.PidFileData) {
				pidData.Port = 0
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pidData := testEventPIDData()
			test.mutate(pidData)
			upstreamCalls := 0
			installEventProxyStubs(
				t,
				func(*http.Request, time.Duration) (*http.Response, error) {
					upstreamCalls++
					return eventUpstreamResponse(http.StatusOK, `{"ok":true}`), nil
				},
			)
			reviewGatewayPIDData = func() *ppid.PidFileData {
				return cloneEventGatewayPIDData(pidData)
			}

			mux := http.NewServeMux()
			NewHandler(configPath).RegisterRoutes(mux)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(
				recorder,
				httptest.NewRequest(
					http.MethodGet,
					"/api/reviews/"+testReviewCaseID,
					nil,
				),
			)

			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf(
					"status = %d, want 503, body=%s",
					recorder.Code,
					recorder.Body.String(),
				)
			}
			if upstreamCalls != 0 {
				t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
			}
			if recorder.Body.String() != "{\"error\":\"review gateway unavailable\"}\n" {
				t.Fatalf("body = %q", recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), pidData.Token) {
				t.Fatal("gateway bearer leaked in error response")
			}
		})
	}
}

func TestReviewProxyPeeksGatewayPIDWithoutLifecycleSideEffects(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	if err := os.MkdirAll(globalConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(globalConfigDir(), ".picoclaw.pid")

	originalReviewPIDData := reviewGatewayPIDData
	originalEventPIDData := eventGatewayPIDData
	originalGatewayDo := eventGatewayDo
	originalProcessMatcher := gatewayProcessMatcher
	originalHealthGet := gatewayHealthGet
	t.Cleanup(func() {
		reviewGatewayPIDData = originalReviewPIDData
		eventGatewayPIDData = originalEventPIDData
		eventGatewayDo = originalGatewayDo
		gatewayProcessMatcher = originalProcessMatcher
		gatewayHealthGet = originalHealthGet
	})
	reviewGatewayPIDData = func() *ppid.PidFileData {
		return ppid.PeekPidFile(globalConfigDir())
	}
	// Any use of the event lifecycle reader would panic and fail the test.
	eventGatewayPIDData = nil
	processChecks := 0
	healthChecks := 0
	upstreamCalls := 0
	gatewayProcessMatcher = func(int) (bool, bool) {
		processChecks++
		return false, true
	}
	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		healthChecks++
		return nil, errors.New("unexpected health probe")
	}
	eventGatewayDo = func(*http.Request, time.Duration) (*http.Response, error) {
		upstreamCalls++
		return nil, errors.New("connection refused")
	}

	tests := []struct {
		name              string
		contents          string
		wantUpstreamCalls int
	}{
		{
			name:              "invalid metadata",
			contents:          `{"pid":`,
			wantUpstreamCalls: 0,
		},
		{
			name: "dead process metadata",
			contents: `{"pid":2147483647,"token":"gateway-pid-token",` +
				`"version":"test","port":18790,"host":"127.0.0.1"}`,
			wantUpstreamCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(pidPath, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			callsBefore := upstreamCalls
			mux := http.NewServeMux()
			NewHandler(configPath).RegisterRoutes(mux)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(
				recorder,
				httptest.NewRequest(
					http.MethodGet,
					"/api/reviews/"+testReviewCaseID+"/attention",
					nil,
				),
			)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
			if got := upstreamCalls - callsBefore; got != test.wantUpstreamCalls {
				t.Fatalf("upstream calls = %d, want %d", got, test.wantUpstreamCalls)
			}
			after, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatalf("PID metadata was removed: %v", err)
			}
			if string(after) != string(before) {
				t.Fatalf("PID metadata changed: before=%q after=%q", before, after)
			}
		})
	}
	if processChecks != 0 || healthChecks != 0 {
		t.Fatalf(
			"review PID peek performed lifecycle checks: process=%d health=%d",
			processChecks,
			healthChecks,
		)
	}
}

func TestReviewMutationsRequireSameOriginJSONIdentityAndBoundedBody(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	upstreamCalls := 0
	installEventProxyStubs(
		t,
		func(*http.Request, time.Duration) (*http.Response, error) {
			upstreamCalls++
			return eventUpstreamResponse(http.StatusOK, `{"ok":true}`), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)
	path := "/api/reviews/" + testReviewCaseID + "/submit"

	tests := []struct {
		name        string
		body        string
		contentType string
		encoding    []string
		fetchSite   string
		origin      string
		referer     string
		query       string
		want        int
	}{
		{
			name:        "missing origin proof",
			body:        `{}`,
			contentType: "application/json",
			want:        http.StatusForbidden,
		},
		{
			name:        "cross site metadata",
			body:        `{}`,
			contentType: "application/json",
			fetchSite:   "cross-site",
			want:        http.StatusForbidden,
		},
		{
			name:        "conflicting origin",
			body:        `{}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			origin:      "https://evil.example",
			want:        http.StatusForbidden,
		},
		{
			name:      "missing content type",
			body:      `{}`,
			fetchSite: "same-origin",
			want:      http.StatusUnsupportedMediaType,
		},
		{
			name:        "wrong content type",
			body:        `{}`,
			contentType: "text/plain",
			fetchSite:   "same-origin",
			want:        http.StatusUnsupportedMediaType,
		},
		{
			name:        "unsupported content encoding",
			body:        `{}`,
			contentType: "application/json",
			encoding:    []string{"gzip"},
			fetchSite:   "same-origin",
			want:        http.StatusUnsupportedMediaType,
		},
		{
			name:        "repeated content encoding",
			body:        `{}`,
			contentType: "application/json",
			encoding:    []string{"identity", "identity"},
			fetchSite:   "same-origin",
			want:        http.StatusUnsupportedMediaType,
		},
		{
			name:        "empty body",
			contentType: "application/json",
			fetchSite:   "same-origin",
			want:        http.StatusBadRequest,
		},
		{
			name:        "malformed JSON",
			body:        `{"expected_version":`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			want:        http.StatusBadRequest,
		},
		{
			name:        "unexpected query",
			body:        `{}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			query:       "?force=true",
			want:        http.StatusBadRequest,
		},
		{
			name:        "oversized body",
			body:        `"` + strings.Repeat("a", reviewProxyRequestMaxBytes) + `"`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			want:        http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				path+test.query,
				strings.NewReader(test.body),
			)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			for _, encoding := range test.encoding {
				request.Header.Add("Content-Encoding", encoding)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.referer != "" {
				request.Header.Set("Referer", test.referer)
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf(
					"status = %d, want %d, body=%s",
					recorder.Code,
					test.want,
					recorder.Body.String(),
				)
			}
		})
	}

	streamed := httptest.NewRequest(
		http.MethodPost,
		path,
		strings.NewReader(
			`"`+strings.Repeat("a", reviewProxyRequestMaxBytes)+`"`,
		),
	)
	streamed.ContentLength = -1
	streamed.Header.Set("Sec-Fetch-Site", "same-origin")
	streamed.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, streamed)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"streamed oversized status = %d, want 413, body=%s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	ambiguousOrigin := httptest.NewRequest(
		http.MethodPost,
		path,
		strings.NewReader(`{"expected_version":1}`),
	)
	ambiguousOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	ambiguousOrigin.Header.Add("Origin", "http://example.com")
	ambiguousOrigin.Header.Add("Origin", "http://example.com")
	ambiguousOrigin.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, ambiguousOrigin)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf(
			"repeated Origin status = %d, want 403, body=%s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		path,
		strings.NewReader(`{"expected_version":1}`),
	)
	request.Host = "launcher.example"
	request.Header.Set("Origin", "http://launcher.example")
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf(
			"matching Origin status = %d, body=%s",
			recorder.Code,
			recorder.Body.String(),
		)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
}

func TestReviewProxyPreservesSafeStatusAndBody(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	installEventProxyStubs(
		t,
		func(*http.Request, time.Duration) (*http.Response, error) {
			response := eventUpstreamResponse(
				http.StatusConflict,
				`{"error":"review changed","version":12}`,
			)
			response.Header.Set("Retry-After", "secret")
			response.Header.Set("X-Internal-Diagnostic", "database path")
			return response, nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(
		recorder,
		httptest.NewRequest(
			http.MethodGet,
			"/api/reviews/"+testReviewCaseID,
			nil,
		),
	)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != `{"error":"review changed","version":12}` {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if recorder.Header().Get("Retry-After") != "" ||
		recorder.Header().Get("X-Internal-Diagnostic") != "" {
		t.Fatalf("unsafe response headers leaked: %#v", recorder.Header())
	}
}

func TestReviewProxyRewritesOnlyCanonicalLocalLocations(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	locations := []string{
		"/runtime/eventing/reviews/" + testReviewCaseID,
		"/runtime/eventing/reviews/" + testReviewCaseID +
			"/findings/" + testReviewFindingID,
		"https://evil.example/runtime/eventing/reviews/" + testReviewCaseID,
		"//evil.example/runtime/eventing/reviews/" + testReviewCaseID,
		"/runtime/eventing/reviews/" + testReviewCaseID + "?token=secret",
		"/runtime/eventing/reviews/%70rc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	call := 0
	installEventProxyStubs(
		t,
		func(*http.Request, time.Duration) (*http.Response, error) {
			response := eventUpstreamResponse(http.StatusCreated, `{"ok":true}`)
			response.Header.Set("Location", locations[call])
			call++
			return response, nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	for index, upstreamLocation := range locations {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(
			recorder,
			httptest.NewRequest(
				http.MethodGet,
				"/api/reviews/"+testReviewCaseID,
				nil,
			),
		)
		if recorder.Code != http.StatusCreated {
			t.Fatalf(
				"%q status = %d, body=%s",
				upstreamLocation,
				recorder.Code,
				recorder.Body.String(),
			)
		}
		want := ""
		switch index {
		case 0:
			want = "/api/reviews/" + testReviewCaseID
		case 1:
			want = "/api/reviews/" + testReviewCaseID +
				"/findings/" + testReviewFindingID
		}
		if got := recorder.Header().Get("Location"); got != want {
			t.Fatalf(
				"rewrite %q = %q, want %q",
				upstreamLocation,
				got,
				want,
			)
		}
	}
}

func TestReviewProxyRejectsUnsafeGatewayResponsesAndDoesNotFollowRedirects(
	t *testing.T,
) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	tests := []struct {
		name     string
		response func() (*http.Response, error)
		want     int
	}{
		{
			name: "network failure",
			response: func() (*http.Response, error) {
				return nil, errors.New("connection reset")
			},
			want: http.StatusServiceUnavailable,
		},
		{
			name: "nil response",
			response: func() (*http.Response, error) {
				return nil, nil
			},
			want: http.StatusBadGateway,
		},
		{
			name: "authorization failure",
			response: func() (*http.Response, error) {
				return eventUpstreamResponse(
					http.StatusUnauthorized,
					`{"error":"Bearer gateway-pid-token"}`,
				), nil
			},
			want: http.StatusServiceUnavailable,
		},
		{
			name: "redirect",
			response: func() (*http.Response, error) {
				response := eventUpstreamResponse(
					http.StatusTemporaryRedirect,
					`{"redirect":true}`,
				)
				response.Header.Set("Location", "https://evil.example")
				return response, nil
			},
			want: http.StatusBadGateway,
		},
		{
			name: "non JSON content type",
			response: func() (*http.Response, error) {
				response := eventUpstreamResponse(http.StatusOK, `{"ok":true}`)
				response.Header.Set("Content-Type", "text/html")
				return response, nil
			},
			want: http.StatusBadGateway,
		},
		{
			name: "malformed JSON body",
			response: func() (*http.Response, error) {
				return eventUpstreamResponse(http.StatusOK, `{"ok":`), nil
			},
			want: http.StatusBadGateway,
		},
		{
			name: "oversized declared response",
			response: func() (*http.Response, error) {
				response := eventUpstreamResponse(http.StatusOK, `{"ok":true}`)
				response.ContentLength = reviewProxyResponseMaxBytes + 1
				return response, nil
			},
			want: http.StatusBadGateway,
		},
		{
			name: "oversized streamed response",
			response: func() (*http.Response, error) {
				body := `"` +
					strings.Repeat("a", reviewProxyResponseMaxBytes) +
					`"`
				response := eventUpstreamResponse(http.StatusOK, body)
				response.ContentLength = -1
				return response, nil
			},
			want: http.StatusBadGateway,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installEventProxyStubs(
				t,
				func(*http.Request, time.Duration) (*http.Response, error) {
					return test.response()
				},
			)
			mux := http.NewServeMux()
			NewHandler(configPath).RegisterRoutes(mux)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(
				recorder,
				httptest.NewRequest(
					http.MethodGet,
					"/api/reviews/"+testReviewCaseID,
					nil,
				),
			)
			if recorder.Code != test.want {
				t.Fatalf(
					"status = %d, want %d, body=%s",
					recorder.Code,
					test.want,
					recorder.Body.String(),
				)
			}
			if recorder.Header().Get("Location") != "" {
				t.Fatalf(
					"unsafe Location leaked: %q",
					recorder.Header().Get("Location"),
				)
			}
			if strings.Contains(recorder.Body.String(), "gateway-pid-token") {
				t.Fatal("gateway bearer leaked in error response")
			}
		})
	}
}
