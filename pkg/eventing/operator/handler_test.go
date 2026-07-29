package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestControllerServesSanitizedOperatorRoutes(t *testing.T) {
	original := testStoredEvent(testEventID)
	replayed := testStoredEvent(testReplayID)
	replayed.Envelope.ReplayOf = testEventID
	store := &fakeStore{
		getResult: original,
		eventPage: eventing.EventPage{
			Events: []eventing.StoredEvent{original},
		},
		dispatchPage: eventing.DispatchMetadataPage{
			Dispatches: []eventing.DispatchMetadata{
				testDispatchMetadata(testDispatch()),
			},
		},
		replayResult: eventing.InsertResult{
			Event:    replayed,
			Inserted: true,
		},
	}
	controller, _ := testController(t, store)

	listResponse := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events?source=github&limit=2",
		"",
		false,
	)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("event list status = %d, body=%s", listResponse.Code, listResponse.Body.String())
	}
	requireNoStoreJSON(t, listResponse)
	assertHTTPBodySecretsAbsent(t, listResponse.Body.String())
	if strings.Contains(listResponse.Body.String(), "large_integer") {
		t.Fatalf("event list exposed payload: %s", listResponse.Body.String())
	}
	listCalls := store.eventFilters()
	if len(listCalls) != 1 ||
		listCalls[0].Source != "github" ||
		listCalls[0].Limit != 2 {
		t.Fatalf("event list filters = %#v", listCalls)
	}

	detailResponse := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events/"+testEventID,
		"",
		false,
	)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("event detail status = %d, body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	requireNoStoreJSON(t, detailResponse)
	assertHTTPBodySecretsAbsent(t, detailResponse.Body.String())
	if strings.Contains(detailResponse.Body.String(), "9007199254740993") {
		t.Fatalf("ordinary detail exposed payload: %s", detailResponse.Body.String())
	}

	payloadResponse := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events/"+testEventID+"/payload",
		"",
		false,
	)
	if payloadResponse.Code != http.StatusOK {
		t.Fatalf("payload status = %d, body=%s", payloadResponse.Code, payloadResponse.Body.String())
	}
	requireNoStoreJSON(t, payloadResponse)
	if got, want := payloadResponse.Body.String(), string(original.Envelope.Payload); got != want {
		t.Fatalf("payload response = %q, want exact %q", got, want)
	}
	if !strings.Contains(payloadResponse.Body.String(), "9007199254740993") {
		t.Fatal("payload response changed the large numeric token")
	}

	dispatchResponse := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"dispatches?event_id="+url.QueryEscape(testEventID)+"&limit=3",
		"",
		false,
	)
	if dispatchResponse.Code != http.StatusOK {
		t.Fatalf(
			"dispatch list status = %d, body=%s",
			dispatchResponse.Code,
			dispatchResponse.Body.String(),
		)
	}
	requireNoStoreJSON(t, dispatchResponse)
	assertHTTPBodySecretsAbsent(t, dispatchResponse.Body.String())
	dispatchCalls := store.dispatchFilters()
	if len(dispatchCalls) != 1 ||
		dispatchCalls[0].EventID != testEventID ||
		dispatchCalls[0].Limit != 3 {
		t.Fatalf("dispatch list filters = %#v", dispatchCalls)
	}

	replayResponse := performRequest(
		controller,
		http.MethodPost,
		RoutePrefix+"events/"+testEventID+"/replay",
		" { } ",
		true,
	)
	if replayResponse.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	requireNoStoreJSON(t, replayResponse)
	if got := replayResponse.Header().Get("Location"); got != RoutePrefix+"events/"+testReplayID {
		t.Fatalf("Location = %q", got)
	}
	assertHTTPBodySecretsAbsent(t, replayResponse.Body.String())
	if strings.Contains(replayResponse.Body.String(), "large_integer") {
		t.Fatalf("replay response exposed payload: %s", replayResponse.Body.String())
	}
	replayCalls := store.replayIDs()
	if len(replayCalls) != 1 || replayCalls[0] != testEventID {
		t.Fatalf("replay calls = %#v", replayCalls)
	}
}

func TestControllerAcceptsValidQueryOrderAndEncoding(t *testing.T) {
	store := &fakeStore{}
	controller, _ := testController(t, store)
	response := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events?type=issues%2Eopened&source=github&limit=7",
		"",
		false,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	calls := store.eventFilters()
	if len(calls) != 1 ||
		calls[0].Type != "issues.opened" ||
		calls[0].Source != "github" ||
		calls[0].Limit != 7 {
		t.Fatalf("filters = %#v", calls)
	}
}

func TestControllerRejectsInvalidQueries(t *testing.T) {
	store := &fakeStore{}
	controller, _ := testController(t, store)
	tests := []string{
		RoutePrefix + "events?unknown=value",
		RoutePrefix + "events?source=",
		RoutePrefix + "events?source=github&source=email",
		RoutePrefix + "events?limit=01",
		RoutePrefix + "events?limit=-1",
		RoutePrefix + "events?limit=101",
		RoutePrefix + "events?source=github;type=issue",
		RoutePrefix + "events?cursor=not-base64",
		RoutePrefix + "events/" + testEventID + "?source=github",
		RoutePrefix + "events/" + testEventID + "/payload?x=1",
		RoutePrefix + "dispatches?event_id=invalid",
		RoutePrefix + "dispatches?status=unknown",
		RoutePrefix + "dispatches?workflow_ref=",
		RoutePrefix + "dispatches?cursor=not-base64",
		RoutePrefix + "events?source=" + strings.Repeat("x", maxQueryBytes),
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			response := performRequest(
				controller,
				http.MethodGet,
				target,
				"",
				false,
			)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
			requireNoStoreJSON(t, response)
		})
	}
	eventCalls := store.eventFilters()
	dispatchCalls := store.dispatchFilters()
	if len(eventCalls) != 0 || len(dispatchCalls) != 0 {
		t.Fatalf(
			"invalid query reached store: event=%d dispatch=%d",
			len(eventCalls),
			len(dispatchCalls),
		)
	}
}

func TestControllerStrictCanonicalPathsAndMethods(t *testing.T) {
	store := &fakeStore{}
	controller, _ := testController(t, store)
	notFound := []string{
		RoutePrefix,
		RoutePrefix + "events/",
		RoutePrefix + "events//payload",
		RoutePrefix + "events/" + testEventID + "/unknown",
		RoutePrefix + "unknown",
		"/runtime/eventing/%65vents",
	}
	for _, target := range notFound {
		response := performRequest(controller, http.MethodGet, target, "", false)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", target, response.Code)
		}
		requireNoStoreJSON(t, response)
	}

	methodTests := []struct {
		method string
		target string
		allow  string
	}{
		{http.MethodPost, RoutePrefix + "events", http.MethodGet},
		{http.MethodPost, RoutePrefix + "events/" + testEventID, http.MethodGet},
		{http.MethodPost, RoutePrefix + "events/" + testEventID + "/payload", http.MethodGet},
		{http.MethodPost, RoutePrefix + "dispatches", http.MethodGet},
		{http.MethodGet, RoutePrefix + "events/" + testEventID + "/replay", http.MethodPost},
	}
	for _, test := range methodTests {
		response := performRequest(controller, test.method, test.target, "", false)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d", test.method, test.target, response.Code)
		}
		if got := response.Header().Get("Allow"); got != test.allow {
			t.Fatalf("Allow = %q, want %q", got, test.allow)
		}
		requireNoStoreJSON(t, response)
	}
}

func TestReplayRequiresExactlyOneBoundedEmptyJSONObject(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType []string
		encoding    []string
	}{
		{name: "missing content type", body: `{}`},
		{name: "wrong content type", body: `{}`, contentType: []string{"text/plain"}},
		{
			name:        "duplicate content type",
			body:        `{}`,
			contentType: []string{"application/json", "application/json"},
		},
		{name: "empty body", contentType: []string{"application/json"}},
		{name: "null", body: `null`, contentType: []string{"application/json"}},
		{name: "array", body: `[]`, contentType: []string{"application/json"}},
		{name: "nonempty object", body: `{"force":true}`, contentType: []string{"application/json"}},
		{name: "two objects", body: `{} {}`, contentType: []string{"application/json"}},
		{name: "trailing data", body: `{}x`, contentType: []string{"application/json"}},
		{
			name:        "oversized",
			body:        `{` + strings.Repeat(" ", maxReplayBodyBytes) + `}`,
			contentType: []string{"application/json"},
		},
		{
			name:        "encoded",
			body:        `{}`,
			contentType: []string{"application/json"},
			encoding:    []string{"gzip"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{}
			controller, _ := testController(t, store)
			request := httptest.NewRequest(
				http.MethodPost,
				RoutePrefix+"events/"+testEventID+"/replay",
				strings.NewReader(test.body),
			)
			request.Header["Content-Type"] = append([]string(nil), test.contentType...)
			request.Header["Content-Encoding"] = append([]string(nil), test.encoding...)
			response := httptest.NewRecorder()
			controller.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
			requireNoStoreJSON(t, response)
			replayCalls := store.replayIDs()
			if len(replayCalls) != 0 {
				t.Fatalf("invalid body triggered replay: %#v", replayCalls)
			}
		})
	}
}

func TestReplayAcceptsJSONUTF8Charset(t *testing.T) {
	replayed := testStoredEvent(testReplayID)
	replayed.Envelope.ReplayOf = testEventID
	store := &fakeStore{
		replayResult: eventing.InsertResult{
			Event:    replayed,
			Inserted: true,
		},
	}
	controller, _ := testController(t, store)
	request := httptest.NewRequest(
		http.MethodPost,
		RoutePrefix+"events/"+testEventID+"/replay",
		strings.NewReader("{}"),
	)
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")
	request.Header.Set("Content-Encoding", "identity")
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestControllerMapsErrorsWithoutLeakingDetailsOrRetrying(t *testing.T) {
	internal := errors.New("database /secret/path is unavailable")
	tests := []struct {
		name       string
		storeError error
		wantStatus int
	}{
		{
			name:       "not found",
			storeError: eventing.ErrNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unavailable",
			storeError: internal,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "closed",
			storeError: eventing.ErrClosed,
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{replayErr: test.storeError}
			controller, _ := testController(t, store)
			response := performRequest(
				controller,
				http.MethodPost,
				RoutePrefix+"events/"+testEventID+"/replay",
				"{}",
				true,
			)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
			requireNoStoreJSON(t, response)
			if strings.Contains(response.Body.String(), "secret") ||
				strings.Contains(response.Body.String(), "database") {
				t.Fatalf("response leaked internal detail: %s", response.Body.String())
			}
			if got := response.Header().Get("Retry-After"); got != "" {
				t.Fatalf("Retry-After = %q, want absent", got)
			}
			if test.wantStatus == http.StatusInternalServerError &&
				!strings.Contains(response.Body.String(), replayUnknownOutcomeMessage) {
				t.Fatalf(
					"body = %q, want unknown-outcome guidance",
					response.Body.String(),
				)
			}
			replayCalls := store.replayIDs()
			if len(replayCalls) != 1 {
				t.Fatalf("Replay call count = %d, want exactly 1", len(replayCalls))
			}
		})
	}
}

func TestInactiveControllerReturnsRetryableJSON(t *testing.T) {
	controller := NewController()
	response := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events",
		"",
		false,
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	requireNoStoreJSON(t, response)
}

func TestPayloadEndpointMapsNotFoundAndUnavailable(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantRetry  string
	}{
		{"not found", eventing.ErrNotFound, http.StatusNotFound, ""},
		{"unavailable", errors.New("offline"), http.StatusServiceUnavailable, "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{getErr: test.err}
			controller, _ := testController(t, store)
			response := performRequest(
				controller,
				http.MethodGet,
				RoutePrefix+"events/"+testEventID+"/payload",
				"",
				false,
			)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Retry-After"); got != test.wantRetry {
				t.Fatalf("Retry-After = %q, want %q", got, test.wantRetry)
			}
		})
	}
}

func assertHTTPBodySecretsAbsent(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{
		testDedupeKey,
		testRoutingLease,
		testDispatchLease,
		"dedupe_key",
		"lease_token",
		`"owner":`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("operator HTTP body contains %q: %s", forbidden, body)
		}
	}
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("operator HTTP body is not JSON: %v", err)
	}
}

func TestControllerCleanup(t *testing.T) {
	controller := NewController()
	if err := controller.Deactivate(context.Background(), Generation{}); err != nil {
		t.Fatalf("Deactivate(zero) error = %v", err)
	}
}
