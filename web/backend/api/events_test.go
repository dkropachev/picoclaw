package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

const (
	testEventID    = "ev_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testReplayID   = "ev_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testDispatchID = "dsp_cccccccccccccccccccccccccccccccc"
)

func TestEventRoutesProxyAllowlistedContractAndKeepHeadersPrivate(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	type capturedRequest struct {
		method        string
		path          string
		query         url.Values
		authorization string
		cookie        string
		browserHeader string
	}
	var captured []capturedRequest
	installEventProxyStubs(t, func(req *http.Request, _ time.Duration) (*http.Response, error) {
		captured = append(captured, capturedRequest{
			method:        req.Method,
			path:          req.URL.Path,
			query:         req.URL.Query(),
			authorization: req.Header.Get("Authorization"),
			cookie:        req.Header.Get("Cookie"),
			browserHeader: req.Header.Get("X-Browser-Only"),
		})
		body := `{"ok":true}`
		if strings.HasSuffix(req.URL.Path, "/payload") {
			body = `{"large":9007199254740993,"small":1e-1000}`
		}
		response := eventUpstreamResponse(http.StatusOK, body)
		response.Header.Set("Set-Cookie", "upstream=secret")
		response.Header.Set("WWW-Authenticate", `Bearer realm="gateway-secret"`)
		response.Header.Set("X-Upstream-Token", "gateway-token")
		return response, nil
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tests := []struct {
		path          string
		upstreamPath  string
		upstreamQuery url.Values
		wantBody      string
	}{
		{
			path:         "/api/events?source=github&connector=primary&type=issues.opened&routing_status=pending&limit=25&cursor=v1_token",
			upstreamPath: "/runtime/eventing/events",
			upstreamQuery: url.Values{
				"source":         {"github"},
				"connector":      {"primary"},
				"type":           {"issues.opened"},
				"routing_status": {"pending"},
				"limit":          {"25"},
				"cursor":         {"v1_token"},
			},
			wantBody: `{"ok":true}`,
		},
		{
			path:         "/api/events/" + testEventID,
			upstreamPath: "/runtime/eventing/events/" + testEventID,
			wantBody:     `{"ok":true}`,
		},
		{
			path:         "/api/events/" + testEventID + "/payload",
			upstreamPath: "/runtime/eventing/events/" + testEventID + "/payload",
			wantBody:     `{"large":9007199254740993,"small":1e-1000}`,
		},
		{
			path: "/api/events/dispatches?event_id=" + testEventID +
				"&workflow_ref=workflows%2Ftriage.yml&status=running&limit=10&cursor=v1_dispatch",
			upstreamPath: "/runtime/eventing/dispatches",
			upstreamQuery: url.Values{
				"event_id":     {testEventID},
				"workflow_ref": {"workflows/triage.yml"},
				"status":       {"running"},
				"limit":        {"10"},
				"cursor":       {"v1_dispatch"},
			},
			wantBody: `{"ok":true}`,
		},
		{
			path:         "/api/events/dispatches/" + testDispatchID,
			upstreamPath: "/runtime/eventing/dispatches/" + testDispatchID,
			wantBody:     `{"ok":true}`,
		},
	}

	for _, tc := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer browser-token")
		req.Header.Set("Cookie", "picoclaw_launcher_auth=browser-cookie")
		req.Header.Set("X-Browser-Only", "do-not-forward")
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body=%s", tc.path, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != tc.wantBody {
			t.Fatalf("%s body = %q, want exact %q", tc.path, got, tc.wantBody)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", tc.path, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("%s Content-Type = %q", tc.path, got)
		}
		for _, header := range []string{"Set-Cookie", "WWW-Authenticate", "X-Upstream-Token"} {
			if got := rec.Header().Get(header); got != "" {
				t.Fatalf("%s leaked upstream %s = %q", tc.path, header, got)
			}
		}

		got := captured[len(captured)-1]
		if got.method != http.MethodGet || got.path != tc.upstreamPath {
			t.Fatalf("%s upstream = %s %s, want GET %s", tc.path, got.method, got.path, tc.upstreamPath)
		}
		if got.query.Encode() != tc.upstreamQuery.Encode() {
			t.Fatalf("%s upstream query = %q, want %q", tc.path, got.query.Encode(), tc.upstreamQuery.Encode())
		}
		if got.authorization != "Bearer gateway-pid-token" {
			t.Fatalf("%s upstream authorization = %q", tc.path, got.authorization)
		}
		if got.cookie != "" || got.browserHeader != "" {
			t.Fatalf("%s forwarded browser headers: cookie=%q browser=%q", tc.path, got.cookie, got.browserHeader)
		}
		if strings.Contains(rec.Body.String(), "gateway-pid-token") {
			t.Fatalf("%s leaked PID token in body", tc.path)
		}
	}

	if len(captured) != len(tests) {
		t.Fatalf("captured %d upstream requests, want %d", len(captured), len(tests))
	}
}

func TestEventRoutesRejectPathsAndQueriesOutsideContract(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	upstreamCalls := 0
	installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
		upstreamCalls++
		return eventUpstreamResponse(http.StatusOK, `{"ok":true}`), nil
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tooLongCursor := strings.Repeat("a", eventProxyCursorMaxBytes+1)
	tests := []string{
		"/api/events?unknown=value",
		"/api/events?source=a&source=b",
		"/api/events?source=",
		"/api/events?source=%20github",
		"/api/events?routing_status=",
		"/api/events?routing_status=RUNNING",
		"/api/events?limit=0",
		"/api/events?limit=01",
		"/api/events?limit=101",
		"/api/events?cursor=",
		"/api/events?cursor=" + tooLongCursor,
		"/api/events?source=a;b",
		"/api/events/not-an-event-id",
		"/api/events/ev_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"/api/events/" + testEventID + "?include_payload=true",
		"/api/events/" + testEventID + "/payload?format=pretty",
		"/api/events/dispatches?event_id=not-an-event",
		"/api/events/dispatches?status=complete",
		"/api/events/dispatches?workflow_ref=",
		"/api/events/dispatches?limit=10&limit=20",
		"/api/events/dispatches?source=github",
		"/api/events/dispatches/not-a-dispatch",
		"/api/events/dispatches/dsp_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
		"/api/events/dispatches/%64sp_cccccccccccccccccccccccccccccccc",
		"/api/events/dispatches/" + testDispatchID + "?include_owner=true",
	}
	for _, path := range tests {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400, body=%s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s did not set no-store", path)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(
		rec,
		httptest.NewRequest(
			http.MethodPost,
			"/api/events/dispatches/"+testDispatchID,
			nil,
		),
	)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf(
			"dispatch detail POST status = %d, want 405, body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("dispatch detail Allow = %q, want GET", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("dispatch detail method rejection did not set no-store")
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid requests reached upstream %d times", upstreamCalls)
	}
}

func TestEventProxyMapsUnavailableAndMalformedUpstreamSafely(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	tests := []struct {
		name       string
		pidData    *ppid.PidFileData
		response   func() *http.Response
		doErr      error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "gateway absent",
			pidData:    nil,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "connection error",
			pidData:    testEventPIDData(),
			doErr:      errors.New("dial failed with gateway-pid-token"),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:    "upstream unauthorized",
			pidData: testEventPIDData(),
			response: func() *http.Response {
				return eventUpstreamResponse(http.StatusUnauthorized, `{"error":"gateway-pid-token rejected"}`)
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:    "upstream forbidden",
			pidData: testEventPIDData(),
			response: func() *http.Response {
				return eventUpstreamResponse(http.StatusForbidden, `{"error":"forbidden"}`)
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:    "wrong content type",
			pidData: testEventPIDData(),
			response: func() *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/plain"}},
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				}
			},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:    "malformed json",
			pidData: testEventPIDData(),
			response: func() *http.Response {
				return eventUpstreamResponse(http.StatusOK, `{"broken":`)
			},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:    "declared oversized response",
			pidData: testEventPIDData(),
			response: func() *http.Response {
				response := eventUpstreamResponse(http.StatusOK, `{"ok":true}`)
				response.ContentLength = eventProxyJSONMaxBytes + 1
				return response
			},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:    "safe upstream not found",
			pidData: testEventPIDData(),
			response: func() *http.Response {
				return eventUpstreamResponse(http.StatusNotFound, `{"error":"event not found"}`)
			},
			wantStatus: http.StatusNotFound,
			wantBody:   `{"error":"event not found"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origPIDData := eventGatewayPIDData
			origDo := eventGatewayDo
			t.Cleanup(func() {
				eventGatewayPIDData = origPIDData
				eventGatewayDo = origDo
			})
			eventGatewayPIDData = func(_ *Handler, _ *config.Config) *ppid.PidFileData {
				return cloneEventGatewayPIDData(tc.pidData)
			}
			eventGatewayDo = func(_ *http.Request, _ time.Duration) (*http.Response, error) {
				if tc.response == nil {
					return nil, tc.doErr
				}
				return tc.response(), tc.doErr
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "gateway-pid-token") {
				t.Fatalf("response leaked gateway token: %s", rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("response did not set no-store")
			}
			if tc.wantStatus == http.StatusServiceUnavailable &&
				rec.Header().Get("Retry-After") != "1" {
				t.Fatal("retryable response did not set Retry-After")
			}
		})
	}
}

func TestEventDispatchDetailPreservesSafeNotFound(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	installEventProxyStubs(t, func(req *http.Request, _ time.Duration) (*http.Response, error) {
		if req.Method != http.MethodGet ||
			req.URL.Path != "/runtime/eventing/dispatches/"+testDispatchID {
			t.Fatalf("upstream request = %s %s", req.Method, req.URL.Path)
		}
		return eventUpstreamResponse(http.StatusNotFound, `{"error":"Not Found"}`), nil
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(
		rec,
		httptest.NewRequest(
			http.MethodGet,
			"/api/events/dispatches/"+testDispatchID,
			nil,
		),
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("dispatch not-found response did not set no-store")
	}
	if got := rec.Body.String(); got != `{"error":"Not Found"}` {
		t.Fatalf("body = %q, want safe upstream not-found body", got)
	}
}

func TestEventPayloadPreservesExactJSONBytes(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	const payload = "{\n  \"large\": 9007199254740993,\n  \"tiny\": 1e-1000\n}\n"
	installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
		return eventUpstreamResponse(http.StatusOK, payload), nil
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/api/events/"+testEventID+"/payload", nil),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != payload {
		t.Fatalf("payload bytes changed:\ngot  %q\nwant %q", got, payload)
	}
}

func TestEventReplayRequiresSafeEmptyJSONAndRewritesLocation(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	upstreamCalls := 0
	installEventProxyStubs(t, func(req *http.Request, _ time.Duration) (*http.Response, error) {
		upstreamCalls++
		if req.Method != http.MethodPost {
			t.Fatalf("upstream method = %s, want POST", req.Method)
		}
		if req.URL.Path != "/runtime/eventing/events/"+testEventID+"/replay" {
			t.Fatalf("upstream path = %q", req.URL.Path)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		if string(body) != "{}" {
			t.Fatalf("upstream body = %q, want canonical empty object", body)
		}
		if req.Header.Get("Authorization") != "Bearer gateway-pid-token" {
			t.Fatalf("upstream auth = %q", req.Header.Get("Authorization"))
		}
		if req.Header.Get("Cookie") != "" {
			t.Fatalf("upstream Cookie = %q, want empty", req.Header.Get("Cookie"))
		}
		response := eventUpstreamResponse(http.StatusCreated, `{"event":{"id":"`+testReplayID+`"}}`)
		response.Header.Set("Location", "/runtime/eventing/events/"+testReplayID)
		return response, nil
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	invalid := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		origin      string
		fetchSite   string
		wantStatus  int
	}{
		{
			name: "wrong method", method: http.MethodGet,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json", body: `{}`, wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name: "cross site", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json", body: `{}`,
			origin: "https://evil.example", fetchSite: "cross-site", wantStatus: http.StatusForbidden,
		},
		{
			name: "missing origin metadata", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json", body: `{}`, wantStatus: http.StatusForbidden,
		},
		{
			name: "missing content type", method: http.MethodPost,
			path: "/api/events/" + testEventID + "/replay",
			body: `{}`, wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "wrong content type", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "text/plain", body: `{}`, wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "unsupported content type parameter", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json; profile=example", body: `{}`,
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "empty body", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json", wantStatus: http.StatusBadRequest,
		},
		{
			name: "null body", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json", body: `null`, wantStatus: http.StatusBadRequest,
		},
		{
			name: "array body", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json", body: `[]`, wantStatus: http.StatusBadRequest,
		},
		{
			name: "nonempty object", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json", body: `{"force":true}`, wantStatus: http.StatusBadRequest,
		},
		{
			name: "trailing json", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json", body: `{} {}`, wantStatus: http.StatusBadRequest,
		},
		{
			name: "query rejected", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay?force=true",
			contentType: "application/json", body: `{}`, wantStatus: http.StatusBadRequest,
		},
		{
			name: "oversized body", method: http.MethodPost,
			path:        "/api/events/" + testEventID + "/replay",
			contentType: "application/json",
			body:        strings.Repeat(" ", eventReplayRequestMaxBytes+1) + `{}`,
			wantStatus:  http.StatusRequestEntityTooLarge,
		},
	}
	for _, tc := range invalid {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Host = "launcher.test"
		if tc.contentType != "" {
			req.Header.Set("Content-Type", tc.contentType)
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
		}
		if tc.method == http.MethodPost &&
			tc.name != "cross site" &&
			tc.name != "missing origin metadata" {
			req.Header.Set("Origin", "http://launcher.test")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.wantStatus {
			t.Fatalf("%s status = %d, want %d, body=%s", tc.name, rec.Code, tc.wantStatus, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s response did not set no-store", tc.name)
		}
	}
	for _, mutate := range []func(*http.Request){
		func(req *http.Request) {
			req.Header["Content-Type"] = []string{"application/json", "application/json"}
		},
		func(req *http.Request) {
			req.Header.Set("Content-Encoding", "gzip")
		},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/events/"+testEventID+"/replay",
			strings.NewReader(`{}`),
		)
		req.Host = "launcher.test"
		req.Header.Set("Origin", "http://launcher.test")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Content-Type", "application/json")
		mutate(req)
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("invalid replay headers status = %d, want 415, body=%s", rec.Code, rec.Body.String())
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid replay requests reached upstream %d times", upstreamCalls)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/events/"+testEventID+"/replay",
		strings.NewReader(" { } \n"),
	)
	req.Host = "launcher.test"
	req.Header.Set("Origin", "http://launcher.test")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer browser-token")
	req.Header.Set("Cookie", "picoclaw_launcher_auth=browser-cookie")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("valid replay status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/api/events/"+testReplayID {
		t.Fatalf("Location = %q, want external event URL", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("valid replay did not set no-store")
	}
	if upstreamCalls != 1 {
		t.Fatalf("valid replay upstream calls = %d, want 1", upstreamCalls)
	}
}

func TestEventReplayRejectsMalformedUpstreamLocation(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
		response := eventUpstreamResponse(http.StatusCreated, `{"event":{"id":"`+testReplayID+`"}}`)
		response.Header.Set("Location", "https://evil.example/runtime/eventing/events/"+testReplayID)
		return response, nil
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/events/"+testEventID+"/replay",
		strings.NewReader(`{}`),
	)
	req.Host = "launcher.test"
	req.Header.Set("Origin", "http://launcher.test")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Fatalf("malformed upstream Location leaked: %q", rec.Header().Get("Location"))
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatalf("ambiguous replay response is retryable: %q", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), eventReplayUnknownOutcomeMessage) {
		t.Fatalf("body = %q, want unknown-outcome guidance", rec.Body.String())
	}
}

func TestEventReplayTransportFailureIsUnknownAndNotRetryable(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
		return nil, errors.New("timeout containing gateway-pid-token")
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/events/"+testEventID+"/replay",
		strings.NewReader(`{}`),
	)
	req.Host = "launcher.test"
	req.Header.Set("Origin", "http://launcher.test")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatalf("ambiguous replay response is retryable: %q", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), eventReplayUnknownOutcomeMessage) {
		t.Fatalf("body = %q, want unknown-outcome guidance", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "timeout") ||
		strings.Contains(rec.Body.String(), "gateway-pid-token") {
		t.Fatalf("response leaked transport detail: %s", rec.Body.String())
	}
}

func TestExternalEventReplayLocationRequiresCanonicalInternalPath(t *testing.T) {
	t.Parallel()

	got, err := externalEventReplayLocation(
		"/runtime/eventing/events/" + testReplayID,
	)
	if err != nil || got != "/api/events/"+testReplayID {
		t.Fatalf("externalEventReplayLocation() = %q, %v", got, err)
	}
	for _, location := range []string{
		"//evil.example/runtime/eventing/events/" + testReplayID,
		"https://evil.example/runtime/eventing/events/" + testReplayID,
		"/runtime/eventing/events/" + testReplayID + "?token=secret",
		"/runtime/eventing/events/" + testReplayID + "#fragment",
		"/runtime/eventing/events/%65v_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		if rewritten, rewriteErr := externalEventReplayLocation(location); rewriteErr == nil {
			t.Fatalf("externalEventReplayLocation(%q) = %q, want error", location, rewritten)
		}
	}
}

func TestEventRoutesRemainBehindDashboardAuthentication(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	upstreamCalls := 0
	installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
		upstreamCalls++
		return eventUpstreamResponse(http.StatusOK, `{"events":[]}`), nil
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	protected := middleware.LauncherDashboardAuth(
		middleware.LauncherDashboardAuthConfig{ExpectedCookie: "dashboard-session"},
		mux,
	)

	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}
	if upstreamCalls != 0 {
		t.Fatal("unauthenticated request reached gateway")
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.AddCookie(&http.Cookie{
		Name:  middleware.LauncherDashboardCookieName,
		Value: "dashboard-session",
	})
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("authenticated upstream calls = %d, want 1", upstreamCalls)
	}
}

func TestLiveEventGatewayPIDDataValidatesCachedFallbackAndReturnsCopy(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	origPIDData := gateway.pidData
	origMatcher := gatewayProcessMatcher
	t.Cleanup(func() {
		gateway.mu.Lock()
		gateway.pidData = origPIDData
		gateway.mu.Unlock()
		gatewayProcessMatcher = origMatcher
	})

	gateway.mu.Lock()
	gateway.pidData = &ppid.PidFileData{
		PID:   os.Getpid(),
		Token: "cached-token",
		Host:  "127.0.0.1",
		Port:  18790,
	}
	gateway.mu.Unlock()
	gatewayProcessMatcher = func(pid int) (bool, bool) {
		return pid == os.Getpid(), true
	}

	h := NewHandler(configPath)
	got := h.liveEventGatewayPIDData(nil)
	if got == nil || got.Token != "cached-token" {
		t.Fatalf("liveEventGatewayPIDData() = %#v", got)
	}
	got.Token = "mutated"
	gateway.mu.Lock()
	cachedToken := gateway.pidData.Token
	gateway.mu.Unlock()
	if cachedToken != "cached-token" {
		t.Fatalf("returned PID data aliases cache, cached token = %q", cachedToken)
	}

	gatewayProcessMatcher = func(int) (bool, bool) { return false, true }
	if stale := h.liveEventGatewayPIDData(nil); stale != nil {
		t.Fatalf("stale cached PID data = %#v, want nil", stale)
	}
}

func TestEventGatewayHTTPClientDisablesProxyAndRedirects(t *testing.T) {
	t.Parallel()

	client := newEventGatewayHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("event gateway transport inherited an HTTP proxy")
	}
	if client.CheckRedirect == nil {
		t.Fatal("event gateway client has no redirect guard")
	}
	if err := client.CheckRedirect(&http.Request{}, []*http.Request{{}}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v, want http.ErrUseLastResponse", err)
	}
}

func installEventProxyStubs(
	t *testing.T,
	do func(*http.Request, time.Duration) (*http.Response, error),
) {
	t.Helper()
	origPIDData := eventGatewayPIDData
	origReadOnlyPIDData := workflowEventReadOnlyGatewayPIDData
	origDo := eventGatewayDo
	t.Cleanup(func() {
		eventGatewayPIDData = origPIDData
		workflowEventReadOnlyGatewayPIDData = origReadOnlyPIDData
		eventGatewayDo = origDo
	})
	eventGatewayPIDData = func(_ *Handler, _ *config.Config) *ppid.PidFileData {
		return testEventPIDData()
	}
	workflowEventReadOnlyGatewayPIDData = func(
		_ *Handler,
		_ *config.Config,
	) *ppid.PidFileData {
		return testEventPIDData()
	}
	eventGatewayDo = do
}

func testEventPIDData() *ppid.PidFileData {
	return &ppid.PidFileData{
		PID:   os.Getpid(),
		Token: "gateway-pid-token",
		Host:  "127.0.0.1",
		Port:  18790,
	}
}

func eventUpstreamResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}
