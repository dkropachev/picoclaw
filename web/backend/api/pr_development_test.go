package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testPRDevelopmentCaseID = "pdc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestPRDevelopmentRoutesProxyExactReadOnlyContract(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	type capturedRequest struct {
		method        string
		path          string
		query         url.Values
		timeout       time.Duration
		authorization string
		cookie        string
		browserHeader string
	}
	var captured []capturedRequest
	installEventProxyStubs(
		t,
		func(request *http.Request, timeout time.Duration) (*http.Response, error) {
			captured = append(captured, capturedRequest{
				method:        request.Method,
				path:          request.URL.Path,
				query:         request.URL.Query(),
				timeout:       timeout,
				authorization: request.Header.Get("Authorization"),
				cookie:        request.Header.Get("Cookie"),
				browserHeader: request.Header.Get("X-Browser-Only"),
			})
			response := eventUpstreamResponse(
				http.StatusOK,
				`{"cases":[],"next_cursor":"9007199254740993"}`,
			)
			response.Header.Set("Set-Cookie", "gateway-secret=cookie")
			return response, nil
		},
	)
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	tests := []struct {
		name         string
		path         string
		upstreamPath string
		query        url.Values
	}{
		{
			name: "list",
			path: prDevelopmentAPIPath +
				"?repository=octo%2Frepo&pull_number=17&limit=25&cursor=v1_token",
			upstreamPath: prDevelopmentRuntimePath,
			query: url.Values{
				"repository":  {"octo/repo"},
				"pull_number": {"17"},
				"limit":       {"25"},
				"cursor":      {"v1_token"},
			},
		},
		{
			name:         "detail",
			path:         prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID,
			upstreamPath: prDevelopmentRuntimePath + "/" + testPRDevelopmentCaseID,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer browser-token")
			request.Header.Set("Cookie", "launcher=browser-secret")
			request.Header.Set("X-Browser-Only", "do-not-forward")
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
			if recorder.Body.String() !=
				`{"cases":[],"next_cursor":"9007199254740993"}` {
				t.Fatalf("body = %q", recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "no-store" ||
				recorder.Header().Get("Content-Type") != "application/json" ||
				recorder.Header().Get("Set-Cookie") != "" {
				t.Fatalf("headers = %#v", recorder.Header())
			}
			got := captured[len(captured)-1]
			if got.method != http.MethodGet ||
				got.path != test.upstreamPath ||
				got.query.Encode() != test.query.Encode() ||
				got.timeout != reviewGatewayRequestTimeout ||
				got.authorization != "Bearer gateway-pid-token" ||
				got.cookie != "" ||
				got.browserHeader != "" {
				t.Fatalf("upstream = %#v", got)
			}
		})
	}
	if len(captured) != len(tests) {
		t.Fatalf("upstream calls = %d, want %d", len(captured), len(tests))
	}
}

func TestPRDevelopmentRoutesRejectMalformedOrMutableRequests(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	upstreamCalls := 0
	installEventProxyStubs(
		t,
		func(*http.Request, time.Duration) (*http.Response, error) {
			upstreamCalls++
			return eventUpstreamResponse(http.StatusOK, `{}`), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	badPaths := []string{
		prDevelopmentAPIPath + "/",
		prDevelopmentAPIPath + "/not-a-case",
		prDevelopmentAPIPath + "/pdc_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		prDevelopmentAPIPath + "/%70dc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID + "/chat",
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID + "?private=1",
		prDevelopmentAPIPath + "?unknown=value",
		prDevelopmentAPIPath + "?repository=octo",
		prDevelopmentAPIPath + "?repository=" + strings.Repeat("a", 127) + "%2F" + strings.Repeat("b", 129),
		prDevelopmentAPIPath + "?pull_number=0",
		prDevelopmentAPIPath + "?pull_number=01",
		prDevelopmentAPIPath + "?pull_number=2147483648",
		prDevelopmentAPIPath + "?pull_number=17&pull_number=18",
		prDevelopmentAPIPath + "?limit=101",
		prDevelopmentAPIPath + "?cursor=",
		prDevelopmentAPIPath + "?cursor=" + strings.Repeat("a", reviewProxyCursorMaxBytes+1),
	}
	for _, path := range badPaths {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, body=%s", path, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s was not marked no-store", path)
		}
	}

	for _, path := range []string{
		prDevelopmentAPIPath,
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID,
	} {
		for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(method, path, strings.NewReader(`{"mutate":true}`))
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusMethodNotAllowed ||
				recorder.Header().Get("Allow") != http.MethodGet {
				t.Fatalf("%s %s = %d %#v", method, path, recorder.Code, recorder.Header())
			}
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid requests reached upstream %d time(s)", upstreamCalls)
	}
}

func TestPRDevelopmentCanonicalPathGuardRejectsServeMuxAliases(t *testing.T) {
	innerCalls := 0
	guarded := GuardPRDevelopmentCanonicalPaths(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			innerCalls++
			w.WriteHeader(http.StatusNoContent)
		},
	))

	for _, target := range []string{
		"/api//pr-development",
		"/api/ignored/../pr-development",
		"/api//pr-development/../config",
		"/api/ignored/../pr-development/../config",
		prDevelopmentAPIPath + "/",
		prDevelopmentAPIPath + "/../config",
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID + "/../../status",
		prDevelopmentAPIPath + "/./" + testPRDevelopmentCaseID,
		prDevelopmentAPIPath + "/%70dc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		recorder := httptest.NewRecorder()
		guarded.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, target, nil),
		)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, body=%s", target, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("Cache-Control") != "no-store" ||
			recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s headers = %#v", target, recorder.Header())
		}
	}

	for _, target := range []string{
		prDevelopmentAPIPath,
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID,
		"/api/status",
		"/api/ignored/../status",
	} {
		recorder := httptest.NewRecorder()
		guarded.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, target, nil),
		)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d", target, recorder.Code)
		}
	}
	if innerCalls != 4 {
		t.Fatalf("inner calls = %d, want 4", innerCalls)
	}
}

func TestPRDevelopmentProxyRejectsRequestBody(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	upstreamCalls := 0
	installEventProxyStubs(
		t,
		func(request *http.Request, _ time.Duration) (*http.Response, error) {
			upstreamCalls++
			_, _ = io.ReadAll(request.Body)
			return eventUpstreamResponse(http.StatusOK, `{}`), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, prDevelopmentAPIPath, strings.NewReader("secret")),
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("body-bearing read reached upstream %d time(s)", upstreamCalls)
	}
}

func TestPRDevelopmentProxyRejectsNonIdentityOrAmbiguousEncoding(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	upstreamCalls := 0
	installEventProxyStubs(
		t,
		func(*http.Request, time.Duration) (*http.Response, error) {
			upstreamCalls++
			return eventUpstreamResponse(http.StatusOK, `{}`), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)
	for name, values := range map[string][]string{
		"compressed": {"gzip"},
		"ambiguous":  {"identity", "identity"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, prDevelopmentAPIPath, nil)
			request.Header["Content-Encoding"] = values
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if upstreamCalls != 0 {
		t.Fatalf("encoded reads reached upstream %d time(s)", upstreamCalls)
	}
}
