package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/prdevelopment"
)

const testPRDevelopmentCaseID = "pdc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestPRDevelopmentMaximumEscapedDetailFitsLauncherResponseBound(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	messageContent := strings.Repeat(
		"<",
		eventing.MaxPRDevelopmentTranscriptBytes/eventing.MaxPRDevelopmentMessagesPerCase,
	)
	messages := make([]prdevelopment.Message, eventing.MaxPRDevelopmentMessagesPerCase)
	for index := range messages {
		messages[index] = prdevelopment.Message{
			ID:        fmt.Sprintf("pdm_%032x", index+1),
			Ordinal:   index,
			Role:      eventing.PRDevelopmentMessageAssistant,
			Content:   messageContent,
			CreatedAt: now,
		}
	}
	repairText := strings.Repeat("<", eventing.MaxPRDevelopmentRepairInstructionBytes)
	attempts := make([]prdevelopment.RepairAttempt, eventing.MaxPRDevelopmentRepairAttempts)
	for index := range attempts {
		attempts[index] = prdevelopment.RepairAttempt{
			ID:                  fmt.Sprintf("pdr_%032x", index+1),
			Ordinal:             index,
			Status:              eventing.PRDevelopmentRepairRecoveryRequired,
			ConversationVersion: eventing.MaxPRDevelopmentMessagesPerCase,
			Instruction:         repairText,
			Summary:             repairText,
			ErrorCode:           eventing.PRDevelopmentRepairErrorRecoveryRequired,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
	}
	repository := "a/" + strings.Repeat("a", prdevelopment.MaximumRepositoryBytes-2)
	maximumURL := func(suffix string) string {
		const maximumURLBytes = 4096
		prefix := "https://example.com/"
		return prefix + strings.Repeat(suffix, maximumURLBytes-len(prefix))
	}
	maximumRef := strings.Repeat("<", 1024)
	maximumSHA := strings.Repeat("a", 64)
	detail := prdevelopment.Detail{
		Case: prdevelopment.CaseDetail{
			CaseSummary: prdevelopment.CaseSummary{
				ID:                   testPRDevelopmentCaseID,
				Repository:           repository,
				PullNumber:           prdevelopment.MaximumPullNumber,
				PullURL:              maximumURL("a"),
				PullAuthor:           strings.Repeat("a", 128),
				PullState:            eventing.PRDevelopmentPullOpen,
				HeadRepository:       repository,
				HeadRef:              maximumRef,
				HeadSHA:              maximumSHA,
				ReviewAuthor:         strings.Repeat("a", 128),
				SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
				CurrentReviewState:   eventing.PRDevelopmentReviewChangesRequested,
				ReviewSubmittedAt:    now,
				ReviewURL:            maximumURL("b"),
				CapturedAt:           now,
			},
			BaseRepository:  repository,
			BaseRef:         maximumRef,
			BaseSHA:         maximumSHA,
			ReviewCommitSHA: maximumSHA,
			Feedback:        strings.Repeat("<", 64<<10),
		},
		ConversationVersion:     eventing.MaxPRDevelopmentMessagesPerCase,
		Messages:                messages,
		RepairUnavailableReason: "runtime_unavailable",
		RepairRevision:          eventing.MaxPRDevelopmentRepairVersion,
		RepairSession: &prdevelopment.RepairSession{
			ID:             "pds_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Revision:       eventing.MaxPRDevelopmentRepairVersion,
			AgentID:        strings.Repeat("a", eventing.MaxPRDevelopmentRepairAgentIDBytes),
			HeadRepository: repository,
			HeadRef:        maximumRef,
			HeadSHA:        maximumSHA,
			Attempts:       attempts,
		},
	}
	errorResponse := struct {
		Error  string                `json:"error"`
		Detail *prdevelopment.Detail `json:"detail,omitempty"`
	}{
		Error:  "development conversation changed; reload before retrying",
		Detail: &detail,
	}
	for name, value := range map[string]any{
		"detail":        detail,
		"error wrapper": errorResponse,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json.Marshal(%s) error = %v", name, err)
		}
		if len(encoded)+1 > reviewProxyResponseMaxBytes {
			t.Fatalf(
				"maximum escaped %s response = %d bytes, launcher limit = %d",
				name,
				len(encoded)+1,
				reviewProxyResponseMaxBytes,
			)
		}
	}
}

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
		{
			name: "attention",
			path: prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID +
				"/attention",
			upstreamPath: prDevelopmentRuntimePath + "/" +
				testPRDevelopmentCaseID + "/attention",
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
	for _, target := range []string{
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID + "/unknown",
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID + "/chat/extra",
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID +
			"/attention/respond/extra",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, body=%s", target, recorder.Code, recorder.Body.String())
		}
	}

	for _, path := range []string{
		prDevelopmentAPIPath,
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID,
		prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID + "/attention",
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
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		for _, suffix := range []string{"/chat", "/attention/respond"} {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				method,
				prDevelopmentAPIPath+"/"+testPRDevelopmentCaseID+suffix,
				nil,
			)
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusMethodNotAllowed ||
				recorder.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("%s %s = %d %#v", method, suffix, recorder.Code, recorder.Header())
			}
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid requests reached upstream %d time(s)", upstreamCalls)
	}
}

func TestPRDevelopmentAttentionResponseProxiesOneHardenedMutation(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	type capturedRequest struct {
		method        string
		path          string
		body          string
		timeout       time.Duration
		authorization string
		contentType   string
		cookie        string
	}
	var captured capturedRequest
	installEventProxyStubs(
		t,
		func(request *http.Request, timeout time.Duration) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read upstream body: %v", err)
			}
			captured = capturedRequest{
				method:        request.Method,
				path:          request.URL.Path,
				body:          string(body),
				timeout:       timeout,
				authorization: request.Header.Get("Authorization"),
				contentType:   request.Header.Get("Content-Type"),
				cookie:        request.Header.Get("Cookie"),
			}
			return eventUpstreamResponse(
				http.StatusOK,
				`{"case_version":2,"status":"continuing","can_respond":false,"turns":[]}`,
			), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	body := `{"expected_case_version":2,"response_token":"sha256:` +
		strings.Repeat("a", 64) + `","response":"Keep the compatibility path."}`
	request := httptest.NewRequest(
		http.MethodPost,
		prDevelopmentAPIPath+"/"+testPRDevelopmentCaseID+"/attention/respond",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Content-Encoding", "identity")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Authorization", "Bearer browser-token")
	request.Header.Set("Cookie", "launcher=browser-secret")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if captured.method != http.MethodPost ||
		captured.path != prDevelopmentRuntimePath+"/"+testPRDevelopmentCaseID+
			"/attention/respond" ||
		captured.body != body || captured.timeout != reviewGatewayAIRequestTimeout ||
		captured.authorization != "Bearer gateway-pid-token" ||
		captured.contentType != "application/json" || captured.cookie != "" {
		t.Fatalf("upstream = %#v", captured)
	}
}

func TestPRDevelopmentAttentionResponseRejectsInvalidIntentBeforeProxy(t *testing.T) {
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
	responsePath := prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID +
		"/attention/respond"
	valid := `{"expected_case_version":0,"response_token":"sha256:` +
		strings.Repeat("a", 64) + `","response":"Proceed."}`
	tests := []struct {
		name       string
		target     string
		body       string
		fetchSite  string
		wantStatus int
	}{
		{
			name:       "missing provenance",
			target:     responsePath,
			body:       valid,
			wantStatus: http.StatusForbidden,
		},
		{
			name: "query", target: responsePath + "?x=1", body: valid,
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "unknown field", target: responsePath,
			body:      strings.TrimSuffix(valid, "}") + `,"run_id":"private"}`,
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "duplicate field", target: responsePath,
			body:      strings.Replace(valid, `"response":`, `"response":"first","response":`, 1),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "bad token", target: responsePath,
			body: strings.Replace(valid, "sha256:"+strings.Repeat("a", 64),
				"sha256:"+strings.Repeat("A", 64), 1),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "version above capacity", target: responsePath,
			body: strings.Replace(valid, `"expected_case_version":0`,
				`"expected_case_version":257`, 1),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "unnormalized response", target: responsePath,
			body:      strings.Replace(valid, "Proceed.", " Proceed. ", 1),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "oversized response", target: responsePath,
			body: strings.Replace(valid, "Proceed.",
				strings.Repeat("x", prDevelopmentChatBytes+1), 1),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				test.target,
				strings.NewReader(test.body),
			)
			request.Header.Set("Content-Type", "application/json")
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d, body=%s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
		})
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid attention responses reached upstream %d time(s)", upstreamCalls)
	}
}

func TestPRDevelopmentChatProxiesOneHardenedMutation(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	type capturedRequest struct {
		method        string
		path          string
		body          string
		timeout       time.Duration
		authorization string
		contentType   string
		cookie        string
		browserHeader string
	}
	var captured capturedRequest
	installEventProxyStubs(
		t,
		func(request *http.Request, timeout time.Duration) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read upstream body: %v", err)
			}
			captured = capturedRequest{
				method:        request.Method,
				path:          request.URL.Path,
				body:          string(body),
				timeout:       timeout,
				authorization: request.Header.Get("Authorization"),
				contentType:   request.Header.Get("Content-Type"),
				cookie:        request.Header.Get("Cookie"),
				browserHeader: request.Header.Get("X-Browser-Only"),
			}
			return eventUpstreamResponse(
				http.StatusOK,
				`{"case":{"id":"`+testPRDevelopmentCaseID+`"},"conversation_version":0,"messages":[]}`,
			), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	body := `{"expected_version":0,"content":"Discuss the retry path."}`
	request := httptest.NewRequest(
		http.MethodPost,
		prDevelopmentAPIPath+"/"+testPRDevelopmentCaseID+"/chat",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Content-Encoding", "identity")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Authorization", "Bearer browser-token")
	request.Header.Set("Cookie", "launcher=browser-secret")
	request.Header.Set("X-Browser-Only", "do-not-forward")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response headers = %#v", recorder.Header())
	}
	if captured.method != http.MethodPost ||
		captured.path != prDevelopmentRuntimePath+"/"+testPRDevelopmentCaseID+"/chat" ||
		captured.body != body ||
		captured.timeout != reviewGatewayAIRequestTimeout ||
		captured.authorization != "Bearer gateway-pid-token" ||
		captured.contentType != "application/json" ||
		captured.cookie != "" ||
		captured.browserHeader != "" {
		t.Fatalf("upstream = %#v", captured)
	}
}

func TestPRDevelopmentRepairProxiesOneHardenedAdmission(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	type capturedRequest struct {
		method        string
		path          string
		body          string
		timeout       time.Duration
		authorization string
		contentType   string
		cookie        string
		browserHeader string
	}
	var captured capturedRequest
	installEventProxyStubs(
		t,
		func(request *http.Request, timeout time.Duration) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read upstream body: %v", err)
			}
			captured = capturedRequest{
				method:        request.Method,
				path:          request.URL.Path,
				body:          string(body),
				timeout:       timeout,
				authorization: request.Header.Get("Authorization"),
				contentType:   request.Header.Get("Content-Type"),
				cookie:        request.Header.Get("Cookie"),
				browserHeader: request.Header.Get("X-Browser-Only"),
			}
			return eventUpstreamResponse(http.StatusAccepted, `{"repair_revision":1}`), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	body := `{"expected_conversation_version":2,"expected_repair_revision":0,"request_id":"prq_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","instruction":"Address the review feedback."}`
	request := httptest.NewRequest(
		http.MethodPost,
		prDevelopmentAPIPath+"/"+testPRDevelopmentCaseID+"/repair",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Content-Encoding", "identity")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Authorization", "Bearer browser-token")
	request.Header.Set("Cookie", "launcher=browser-secret")
	request.Header.Set("X-Browser-Only", "do-not-forward")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response headers = %#v", recorder.Header())
	}
	if captured.method != http.MethodPost ||
		captured.path != prDevelopmentRuntimePath+"/"+testPRDevelopmentCaseID+"/repair" ||
		captured.body != body ||
		captured.timeout != reviewGatewayRequestTimeout ||
		captured.authorization != "Bearer gateway-pid-token" ||
		captured.contentType != "application/json" ||
		captured.cookie != "" ||
		captured.browserHeader != "" {
		t.Fatalf("upstream = %#v", captured)
	}
}

func TestPRDevelopmentRepairRejectsInvalidAdmissionBeforeProxy(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	upstreamCalls := 0
	installEventProxyStubs(
		t,
		func(*http.Request, time.Duration) (*http.Response, error) {
			upstreamCalls++
			return eventUpstreamResponse(http.StatusAccepted, `{}`), nil
		},
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)
	repairPath := prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID + "/repair"
	valid := `{"expected_conversation_version":0,"expected_repair_revision":0,"request_id":"prq_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","instruction":"Fix it."}`
	tests := []struct {
		name       string
		target     string
		body       string
		fetchSite  string
		wantStatus int
	}{
		{
			name: "missing provenance", target: repairPath, body: valid,
			wantStatus: http.StatusForbidden,
		},
		{
			name: "query", target: repairPath + "?x=1", body: valid,
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "unknown field", target: repairPath,
			body:      strings.TrimSuffix(valid, "}") + `,"extra":true}`,
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "duplicate field", target: repairPath,
			body: strings.Replace(
				valid, `"instruction":`, `"instruction":"first","instruction":`, 1,
			),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "bad request id", target: repairPath,
			body: strings.Replace(
				valid,
				"prq_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"prq_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				1,
			),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "stale conversation range", target: repairPath,
			body: strings.Replace(
				valid,
				`"expected_conversation_version":0`,
				`"expected_conversation_version":257`,
				1,
			),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "negative repair revision", target: repairPath,
			body: strings.Replace(
				valid,
				`"expected_repair_revision":0`,
				`"expected_repair_revision":-1`,
				1,
			),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "blank instruction", target: repairPath,
			body:      strings.Replace(valid, "Fix it.", " ", 1),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
		{
			name: "oversized instruction", target: repairPath,
			body: strings.Replace(
				valid,
				"Fix it.",
				strings.Repeat("a", prDevelopmentRepairBytes+1),
				1,
			),
			fetchSite: "same-origin", wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid requests reached upstream %d time(s)", upstreamCalls)
	}
}

func TestPRDevelopmentChatRejectsUnsafeMutationBeforeProxy(t *testing.T) {
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
	chatPath := prDevelopmentAPIPath + "/" + testPRDevelopmentCaseID + "/chat"

	tests := []struct {
		name        string
		target      string
		body        string
		contentType string
		fetchSite   string
		origin      string
		encoding    []string
		streamed    bool
		wantStatus  int
	}{
		{
			name: "missing same origin evidence", target: chatPath, body: `{}`,
			contentType: "application/json", wantStatus: http.StatusForbidden,
		},
		{
			name:        "cross site",
			target:      chatPath,
			body:        `{}`,
			contentType: "application/json",
			fetchSite:   "cross-site",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "foreign origin",
			target:      chatPath,
			body:        `{}`,
			contentType: "application/json",
			origin:      "https://evil.example",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "query",
			target:      chatPath + "?private=1",
			body:        `{}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "bare query",
			target:      chatPath + "?",
			body:        `{}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:       "missing media type",
			target:     chatPath,
			body:       `{}`,
			fetchSite:  "same-origin",
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:        "wrong media type",
			target:      chatPath,
			body:        `{}`,
			contentType: "text/plain",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "compressed",
			target:      chatPath,
			body:        `{}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			encoding:    []string{"gzip"},
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "ambiguous encoding",
			target:      chatPath,
			body:        `{}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			encoding:    []string{"identity", "identity"},
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "empty body",
			target:      chatPath,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "malformed JSON",
			target:      chatPath,
			body:        `{`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "two JSON values",
			target:      chatPath,
			body:        `{} {}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "duplicate field",
			target:      chatPath,
			body:        `{"expected_version":0,"content":"one","content":"two"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "case folded duplicate",
			target:      chatPath,
			body:        `{"expected_version":0,"content":"one","Content":"two"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "case folded aliases",
			target:      chatPath,
			body:        `{"Expected_Version":0,"CONTENT":"one"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "unknown field",
			target:      chatPath,
			body:        `{"expected_version":0,"content":"one","tools":true}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "fractional version",
			target:      chatPath,
			body:        `{"expected_version":0.5,"content":"one"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "negative version",
			target:      chatPath,
			body:        `{"expected_version":-1,"content":"one"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "version above capacity",
			target:      chatPath,
			body:        `{"expected_version":257,"content":"one"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "maximum int64 version",
			target:      chatPath,
			body:        `{"expected_version":9223372036854775807,"content":"one"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "blank content",
			target:      chatPath,
			body:        `{"expected_version":0,"content":"  "}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "NUL content",
			target:      chatPath,
			body:        `{"expected_version":0,"content":"bad\u0000value"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "oversized content",
			target:      chatPath,
			body:        `{"expected_version":0,"content":"` + strings.Repeat("x", prDevelopmentChatBytes+1) + `"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "unpaired surrogate",
			target:      chatPath,
			body:        `{"expected_version":0,"content":"\uD800"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "invalid UTF-8",
			target:      chatPath,
			body:        string([]byte{'{', '"', 0xff, '"', ':', '0', '}'}),
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:   "excessive depth",
			target: chatPath,
			body: `{"expected_version":0,"content":"one","nested":` + strings.Repeat(
				"[",
				prDevelopmentChatDepth+1,
			) + `0` + strings.Repeat(
				"]",
				prDevelopmentChatDepth+1,
			) + `}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "streamed",
			target:      chatPath,
			body:        `{"expected_version":0,"content":"one"}`,
			contentType: "application/json",
			fetchSite:   "same-origin",
			streamed:    true,
			wantStatus:  http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				test.target,
				strings.NewReader(test.body),
			)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.encoding != nil {
				request.Header["Content-Encoding"] = test.encoding
			}
			if test.streamed {
				request.ContentLength = -1
				request.TransferEncoding = []string{"chunked"}
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "no-store" ||
				recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("headers = %#v", recorder.Header())
			}
		})
	}

	oversized := httptest.NewRequest(
		http.MethodPost,
		chatPath,
		strings.NewReader(`"`+strings.Repeat("a", reviewProxyRequestMaxBytes)+`"`),
	)
	oversized.Header.Set("Content-Type", "application/json")
	oversized.Header.Set("Sec-Fetch-Site", "same-origin")
	oversizedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(oversizedRecorder, oversized)
	if oversizedRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, body=%s", oversizedRecorder.Code, oversizedRecorder.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("unsafe mutations reached upstream %d time(s)", upstreamCalls)
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
