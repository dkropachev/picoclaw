package reviews

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderHandlerProjectsUnavailableCaseScopedStatusAndSnapshot(t *testing.T) {
	for _, test := range []struct {
		name       string
		path       string
		statusOnly bool
	}{
		{name: "status", path: RuntimeRoutePrefix + "/" + serviceTestCaseID + "/provider?view=status", statusOnly: true},
		{name: "snapshot", path: RuntimeRoutePrefix + "/" + serviceTestCaseID + "/provider"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &reviewServiceStore{getResult: serviceTestDetail(4)}
			handler := &Handler{Service: newReviewTestService(t, store, nil, nil)}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["availability"] != ProviderAvailabilityUnavailable || body["connector"] != "github-main" ||
				body["repository"] != "scylladb/gocql" || body["pull_number"] != float64(42) {
				t.Fatalf("body=%#v", body)
			}
			_, hasReviews := body["reviews"]
			if hasReviews == test.statusOnly {
				t.Fatalf("statusOnly=%v body=%#v", test.statusOnly, body)
			}
			if test.statusOnly {
				limitations, ok := body["limitations"].([]any)
				if !ok || len(limitations) != 1 || limitations[0] != providerLimitationStatusView {
					t.Fatalf("limitations=%#v", body["limitations"])
				}
			}
		})
	}
}

func TestProviderHandlerRequiresExactStatusQueryAndThreadBody(t *testing.T) {
	store := &reviewServiceStore{getResult: serviceTestDetail(4)}
	handler := &Handler{Service: newReviewTestService(t, store, nil, nil)}
	for _, query := range []string{"?", "?view=", "?view=status&", "?view=status&view=status", "?other=status"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RuntimeRoutePrefix+"/"+serviceTestCaseID+"/provider"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query %q status=%d body=%s", query, response.Code, response.Body.String())
		}
	}

	path := RuntimeRoutePrefix + "/" + serviceTestCaseID + "/provider/thread"
	token := "rtt_" + strings.Repeat("A", 43)
	for _, body := range []string{
		`{}`,
		`{"token":"` + token + `","action":"reopen"}`,
		`{"token":"` + token + `","token":"` + token + `","action":"resolve"}`,
		`{"token":"` + token + `","action":"resolve","extra":true}`,
	} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"token":"`+token+`","action":"resolve"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || strings.Contains(response.Body.String(), "runner") {
		t.Fatalf("valid unsupported mutation status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProviderHandlerMethodContracts(t *testing.T) {
	handler := &Handler{Service: newReviewTestService(t, &reviewServiceStore{getResult: serviceTestDetail(4)}, nil, nil)}
	for _, test := range []struct{ method, path, allow string }{
		{http.MethodPost, RuntimeRoutePrefix + "/" + serviceTestCaseID + "/provider", http.MethodGet},
		{http.MethodGet, RuntimeRoutePrefix + "/" + serviceTestCaseID + "/provider/thread", http.MethodPost},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != test.allow {
			t.Fatalf("%s %s = %d allow=%q", test.method, test.path, response.Code, response.Header().Get("Allow"))
		}
	}
}
