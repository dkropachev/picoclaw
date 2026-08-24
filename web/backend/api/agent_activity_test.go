package api

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentloop "github.com/sipeed/picoclaw/pkg/agent"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const (
	validAgentActivityCursor            = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	validAgentActivityCursorSequenceTwo = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAC"
)

type agentActivityStringAddress string

func (address agentActivityStringAddress) Network() string { return "test" }
func (address agentActivityStringAddress) String() string  { return string(address) }

func TestAgentActivityHelperCoverageMargin(t *testing.T) {
	if _, ok := launcherAgentActivityID(nil); ok {
		t.Fatal("nil request produced an agent identity")
	}
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/agents/main/activity", nil),
		httptest.NewRequest(http.MethodGet, "/wrong/main/activity", nil),
		httptest.NewRequest(http.MethodGet, "/api/agents/main/other", nil),
		httptest.NewRequest(http.MethodGet, "/api/agents/NotValid/activity", nil),
	}
	requests[0].URL.RawPath = "/api/agents/main%2Factivity"
	for index, request := range requests {
		if id, ok := launcherAgentActivityID(request); ok {
			t.Errorf("invalid request %d produced agent %q", index, id)
		}
	}

	original := agentActivityInterfaceAddrs
	defer func() { agentActivityInterfaceAddrs = original }()
	agentActivityInterfaceAddrs = func() ([]net.Addr, error) {
		return nil, errors.New("injected interface failure")
	}
	if localAgentActivityHost("192.0.2.44") {
		t.Fatal("interface failure accepted a non-loopback host")
	}
	agentActivityInterfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			agentActivityStringAddress("192.0.2.44/24"),
			&net.IPAddr{IP: net.ParseIP("2001:db8::44")},
			agentActivityStringAddress("not-a-cidr"),
		}, nil
	}
	for _, host := range []string{"192.0.2.44", "2001:db8::44"} {
		if !localAgentActivityHost(host) {
			t.Errorf("assigned host %q was rejected", host)
		}
	}
	if localAgentActivityHost("192.0.2.45") {
		t.Fatal("unassigned host was accepted")
	}
}

func TestAgentActivityProxyUsesIsolatedTransportAndCanonicalResponse(t *testing.T) {
	originalPID := agentActivityGatewayPIDData
	originalDo := agentActivityGatewayDo
	defer func() {
		agentActivityGatewayPIDData = originalPID
		agentActivityGatewayDo = originalDo
	}()

	received := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime/agents/main/activity" ||
			r.URL.RawQuery != "limit=10" {
			t.Errorf("upstream URL = %s", r.URL.String())
		}
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, validAgentActivityPageJSON("main"))
	}))
	defer upstream.Close()
	host, rawPort, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, _ := strconv.Atoi(rawPort)
	agentActivityGatewayPIDData = func() *ppid.PidFileData {
		return &ppid.PidFileData{
			PID:   42,
			Host:  host,
			Port:  port,
			Token: "runtime-token",
		}
	}
	agentActivityGatewayDo = originalDo

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/agents/main/activity?limit=10",
		nil,
	)
	request.SetPathValue("id", "main")
	request.Header.Set("Authorization", "Bearer browser-secret")
	request.Header.Set("Cookie", "session=browser-secret")
	request.Header.Set("X-Forwarded-For", "203.0.113.20")
	request.Header.Set("X-Trace-Secret", "browser-secret")
	recorder := httptest.NewRecorder()
	NewHandler("").handleGetAgentActivity(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", recorder.Header())
	}
	page, err := agentloop.DecodeAgentActivityPage(recorder.Body.Bytes())
	if err != nil || page.AgentID != "main" {
		t.Fatalf("response page = %#v, error = %v", page, err)
	}

	select {
	case headers := <-received:
		if got := headers.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q", got)
		}
		if got := headers.Get("Authorization"); got != "Bearer runtime-token" {
			t.Fatalf("Authorization = %q", got)
		}
		for name := range headers {
			if name != "Accept" && name != "Authorization" {
				t.Fatalf("unexpected wire header %q: %#v", name, headers)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request was not received")
	}
}

func TestAgentActivityProxyRegistrationAndRequestValidation(t *testing.T) {
	originalPID := agentActivityGatewayPIDData
	originalDo := agentActivityGatewayDo
	defer func() {
		agentActivityGatewayPIDData = originalPID
		agentActivityGatewayDo = originalDo
	}()
	agentActivityGatewayPIDData = func() *ppid.PidFileData {
		return &ppid.PidFileData{
			PID:   42,
			Host:  "127.0.0.1",
			Port:  18789,
			Token: "runtime-token",
		}
	}
	var calls atomic.Int64
	agentActivityGatewayDo = func(*http.Request, time.Duration) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	}

	tests := []struct {
		name   string
		method string
		target string
		pathID string
		status int
		error  string
	}{
		{
			name:   "method",
			method: http.MethodPost,
			target: "/api/agents/main/activity",
			pathID: "main",
			status: http.StatusMethodNotAllowed,
			error:  "method_not_allowed",
		},
		{
			name:   "noncanonical agent",
			method: http.MethodGet,
			target: "/api/agents/Main/activity",
			pathID: "Main",
			status: http.StatusNotFound,
			error:  "agent_not_found",
		},
		{
			name:   "wrong exact path",
			method: http.MethodGet,
			target: "/api/agents/main/activity/",
			pathID: "main",
			status: http.StatusNotFound,
			error:  "agent_not_found",
		},
		{
			name:   "unknown query",
			method: http.MethodGet,
			target: "/api/agents/main/activity?secret=1",
			pathID: "main",
			status: http.StatusBadRequest,
			error:  "invalid_agent_activity_query",
		},
		{
			name:   "duplicate limit",
			method: http.MethodGet,
			target: "/api/agents/main/activity?limit=1&limit=2",
			pathID: "main",
			status: http.StatusBadRequest,
			error:  "invalid_agent_activity_query",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.target, nil)
			request.SetPathValue("id", test.pathID)
			recorder := httptest.NewRecorder()
			NewHandler("").handleGetAgentActivity(recorder, request)
			if recorder.Code != test.status ||
				recorder.Body.String() != `{"error":"`+test.error+`"}`+"\n" {
				t.Fatalf(
					"response = %d %q",
					recorder.Code,
					recorder.Body.String(),
				)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}

	mux := http.NewServeMux()
	NewHandler("").RegisterRoutes(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(
		recorder,
		httptest.NewRequest(
			http.MethodGet,
			"/api/agents/main/activity",
			nil,
		),
	)
	if recorder.Code != http.StatusServiceUnavailable ||
		recorder.Body.String() !=
			"{\"error\":\"agent_activity_unavailable\"}\n" {
		t.Fatalf(
			"registered response = %d %q",
			recorder.Code,
			recorder.Body.String(),
		)
	}
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/agents/main/activity",
			nil,
		),
	)
	if recorder.Code != http.StatusMethodNotAllowed ||
		recorder.Body.String() != "{\"error\":\"method_not_allowed\"}\n" ||
		recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf(
			"registered method response = %d %q %#v",
			recorder.Code,
			recorder.Body.String(),
			recorder.Header(),
		)
	}
}

func TestLocalAgentActivityHostRequiresLiteralAssignedAddress(t *testing.T) {
	original := agentActivityInterfaceAddrs
	defer func() {
		agentActivityInterfaceAddrs = original
	}()
	agentActivityInterfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{
				IP:   net.ParseIP("192.0.2.44"),
				Mask: net.CIDRMask(24, 32),
			},
			&net.IPNet{
				IP:   net.ParseIP("2001:db8::44"),
				Mask: net.CIDRMask(64, 128),
			},
		}, nil
	}
	tests := map[string]bool{
		"127.0.0.1":    true,
		"::1":          true,
		"192.0.2.44":   true,
		"2001:db8::44": true,
		"192.0.2.45":   false,
		"localhost":    false,
		"LOCALHOST":    false,
		"example.com":  false,
		"0.0.0.0":      false,
		"::":           false,
		"*":            false,
		"":             false,
	}
	for host, want := range tests {
		if got := localAgentActivityHost(host); got != want {
			t.Errorf("localAgentActivityHost(%q) = %t, want %t", host, got, want)
		}
	}
}

func TestAgentActivityGatewayURLRejectsLegacyLocalhostSentinel(
	t *testing.T,
) {
	pidData := &ppid.PidFileData{
		PID:   2,
		Host:  "localhost",
		Port:  18789,
		Token: "token",
	}
	if _, ok := agentActivityGatewayURL(
		pidData,
		"main",
		"",
		agentloop.DefaultAgentActivityLimit,
	); ok {
		t.Fatal("agentActivityGatewayURL() accepted ambiguous localhost authority")
	}
	if pidData.Host != "localhost" {
		t.Fatalf("PID authority was mutated to %q", pidData.Host)
	}
}

func TestAgentActivityGatewayURLAcceptsNumericIPv6Loopback(t *testing.T) {
	target, ok := agentActivityGatewayURL(
		&ppid.PidFileData{
			PID:   2,
			Host:  "::1",
			Port:  18789,
			Token: "token",
		},
		"main",
		"",
		agentloop.DefaultAgentActivityLimit,
	)
	if !ok {
		t.Fatal("agentActivityGatewayURL() rejected numeric IPv6 loopback")
	}
	if target.Host != "[::1]:18789" {
		t.Fatalf("target host = %q, want [::1]:18789", target.Host)
	}
}

func TestAgentActivityPIDDataValidationFailsClosed(t *testing.T) {
	original := agentActivityInterfaceAddrs
	defer func() {
		agentActivityInterfaceAddrs = original
	}()
	agentActivityInterfaceAddrs = func() ([]net.Addr, error) {
		return nil, errors.New("interfaces unavailable")
	}
	valid := &ppid.PidFileData{
		PID:   2,
		Host:  "127.0.0.1",
		Port:  18789,
		Token: "token",
	}
	if !validAgentActivityPIDData(valid) {
		t.Fatal("valid loopback PID data rejected")
	}
	tests := []*ppid.PidFileData{
		nil,
		{PID: 0, Host: "127.0.0.1", Port: 18789, Token: "token"},
		{PID: 2, Host: "127.0.0.1 ", Port: 18789, Token: "token"},
		{PID: 2, Host: "localhost", Port: 18789, Token: "token"},
		{PID: 2, Host: "127.0.0.1", Port: 0, Token: "token"},
		{PID: 2, Host: "127.0.0.1", Port: 65536, Token: "token"},
		{PID: 2, Host: "127.0.0.1", Port: 18789, Token: ""},
		{PID: 2, Host: "127.0.0.1", Port: 18789, Token: " token"},
		{PID: 2, Host: "127.0.0.1", Port: 18789, Token: "bad\nheader"},
	}
	for index, pidData := range tests {
		if validAgentActivityPIDData(pidData) {
			t.Errorf("invalid PID case %d accepted: %#v", index, pidData)
		}
	}
}

func TestAgentActivityProxyRejectsUntrustedUpstreamResponses(t *testing.T) {
	originalPID := agentActivityGatewayPIDData
	originalDo := agentActivityGatewayDo
	defer func() {
		agentActivityGatewayPIDData = originalPID
		agentActivityGatewayDo = originalDo
	}()
	agentActivityGatewayPIDData = func() *ppid.PidFileData {
		return &ppid.PidFileData{
			PID:   42,
			Host:  "127.0.0.1",
			Port:  18789,
			Token: "runtime-token",
		}
	}

	tests := []struct {
		name          string
		target        string
		status        int
		header        http.Header
		body          string
		contentLength int64
		wantStatus    int
		wantError     string
	}{
		{
			name:       "upstream bad request",
			status:     http.StatusBadRequest,
			body:       `{"error":"secret upstream detail"}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_agent_activity_query",
		},
		{
			name:       "upstream not found",
			status:     http.StatusNotFound,
			body:       `{"error":"secret upstream detail"}`,
			wantStatus: http.StatusNotFound,
			wantError:  "agent_not_found",
		},
		{
			name:       "unauthorized",
			status:     http.StatusUnauthorized,
			body:       `{"error":"secret upstream detail"}`,
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
		{
			name:       "redirect",
			status:     http.StatusFound,
			body:       "secret redirect",
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
		{
			name:       "missing content type",
			status:     http.StatusOK,
			body:       validAgentActivityPageJSON("main"),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
		{
			name:   "duplicate content type",
			status: http.StatusOK,
			header: http.Header{
				"Content-Type": {"application/json"},
				"content-type": {"application/json"},
			},
			body:       validAgentActivityPageJSON("main"),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
		{
			name:       "wrong agent",
			status:     http.StatusOK,
			header:     http.Header{"Content-Type": {"application/json"}},
			body:       validAgentActivityPageJSON("other"),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
		{
			name:   "unknown JSON member",
			status: http.StatusOK,
			header: http.Header{"Content-Type": {"application/json"}},
			body: strings.Replace(
				validAgentActivityPageJSON("main"),
				`"events"`,
				`"secret":"private","events"`,
				1,
			),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
		{
			name:   "duplicate case folded JSON member",
			status: http.StatusOK,
			header: http.Header{"Content-Type": {"application/json"}},
			body: strings.Replace(
				validAgentActivityPageJSON("main"),
				`"agent_id":"main"`,
				`"agent_id":"main","Agent_ID":"private"`,
				1,
			),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
		{
			name:       "more events than requested",
			target:     "/api/agents/main/activity?limit=1",
			status:     http.StatusOK,
			header:     http.Header{"Content-Type": {"application/json"}},
			body:       validAgentActivityTwoEventPageJSON("main"),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
		{
			name:          "declared oversized",
			status:        http.StatusOK,
			header:        http.Header{"Content-Type": {"application/json"}},
			body:          validAgentActivityPageJSON("main"),
			contentLength: agentActivityResponseMaxBytes + 1,
			wantStatus:    http.StatusServiceUnavailable,
			wantError:     "agent_activity_unavailable",
		},
		{
			name:       "streamed oversized",
			status:     http.StatusOK,
			header:     http.Header{"Content-Type": {"application/json"}},
			body:       strings.Repeat("x", agentActivityResponseMaxBytes+1),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "agent_activity_unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agentActivityGatewayDo = func(
				request *http.Request,
				timeout time.Duration,
			) (*http.Response, error) {
				if timeout != agentActivityGatewayTimeout {
					t.Errorf("timeout = %s", timeout)
				}
				if request.Header.Get("Authorization") != "Bearer runtime-token" {
					t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
				}
				contentLength := test.contentLength
				if contentLength == 0 {
					contentLength = int64(len(test.body))
				}
				return &http.Response{
					StatusCode:    test.status,
					Header:        test.header.Clone(),
					Body:          io.NopCloser(strings.NewReader(test.body)),
					ContentLength: contentLength,
				}, nil
			}
			target := test.target
			if target == "" {
				target = "/api/agents/main/activity"
			}
			request := httptest.NewRequest(
				http.MethodGet,
				target,
				nil,
			)
			request.SetPathValue("id", "main")
			recorder := httptest.NewRecorder()
			NewHandler("").handleGetAgentActivity(recorder, request)
			if recorder.Code != test.wantStatus ||
				recorder.Body.String() !=
					`{"error":"`+test.wantError+`"}`+"\n" {
				t.Fatalf(
					"response = %d %q",
					recorder.Code,
					recorder.Body.String(),
				)
			}
			if bytes.Contains(recorder.Body.Bytes(), []byte("private")) ||
				bytes.Contains(recorder.Body.Bytes(), []byte("secret")) {
				t.Fatalf("response leaked upstream data: %s", recorder.Body.Bytes())
			}
		})
	}
}

func TestAgentActivityHTTPClientDisablesProxyCompressionAndRedirects(t *testing.T) {
	t.Parallel()

	client := newAgentActivityGatewayHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("transport inherited an HTTP proxy")
	}
	if !transport.DisableCompression {
		t.Fatal("transport compression is enabled")
	}
	if client.CheckRedirect == nil ||
		!errors.Is(
			client.CheckRedirect(&http.Request{}, []*http.Request{{}}),
			http.ErrUseLastResponse,
		) {
		t.Fatal("client does not reject redirects")
	}
}

func validAgentActivityPageJSON(agentID string) string {
	return `{"agent_id":"` + agentID +
		`","events":[],"next_cursor":"` + validAgentActivityCursor +
		`","reset":false,"truncated":false,` +
		`"dropped":{"subscription":"0","retention":"0","projection":"0"}}`
}

func validAgentActivityTwoEventPageJSON(agentID string) string {
	event := func(sequence string) string {
		return `{"sequence":"` + sequence + `","agent_id":"` + agentID +
			`","timestamp":"2026-07-30T12:00:00Z","kind":"agent.turn.start",` +
			`"severity":"info","details":{"media_count":0}}`
	}
	return `{"agent_id":"` + agentID +
		`","events":[` + event("1") + `,` + event("2") +
		`],"next_cursor":"` + validAgentActivityCursorSequenceTwo +
		`","reset":false,"truncated":true,` +
		`"dropped":{"subscription":"0","retention":"0","projection":"0"}}`
}
