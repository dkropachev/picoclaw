package operator

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControllerDelegatesProtectedPRWorkspaceSubtreeToActiveGeneration(t *testing.T) {
	handler := &recordingPRWorkspaceHandler{}
	backend, err := NewBackend(BackendConfig{
		Store:        &fakeStore{},
		PRWorkspaces: handler,
	})
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	controller := NewController()
	generation, err := controller.Activate(backend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Deactivate(context.Background(), generation)
	})

	request := httptest.NewRequest(
		http.MethodPost,
		RoutePrefix+"pr-workspaces/prw_11111111111111111111111111111111/review?trace=kept",
		strings.NewReader(`{"expected_version":7}`),
	)
	request.Header.Set("X-Delegation-Test", "preserved")
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-PR-Workspaces-Generation"); got != "active" {
		t.Fatalf("generation header = %q", got)
	}
	if handler.calls != 1 || handler.method != http.MethodPost ||
		handler.path != RoutePrefix+"pr-workspaces/prw_11111111111111111111111111111111/review" ||
		handler.rawQuery != "trace=kept" ||
		handler.body != `{"expected_version":7}` ||
		handler.requestHeader != "preserved" {
		t.Fatalf("delegated request = %#v", handler)
	}
}

func TestControllerRejectsInvalidOrUnconfiguredPRWorkspaceRoutes(t *testing.T) {
	handler := &recordingPRWorkspaceHandler{}
	backend, err := NewBackend(BackendConfig{
		Store:        &fakeStore{},
		PRWorkspaces: handler,
	})
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	controller := NewController()
	generation, err := controller.Activate(backend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}

	response := httptest.NewRecorder()
	controller.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, RoutePrefix+"pr-workspaces%2fprw_11111111111111111111111111111111", nil),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("encoded path status = %d", response.Code)
	}
	if handler.calls != 0 {
		t.Fatalf("invalid encoded path delegated %d times", handler.calls)
	}
	if err = controller.Deactivate(context.Background(), generation); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}

	withoutHandler, err := NewBackend(BackendConfig{Store: &fakeStore{}})
	if err != nil {
		t.Fatalf("NewBackend(without handler) error = %v", err)
	}
	generation, err = controller.Activate(withoutHandler)
	if err != nil {
		t.Fatalf("Activate(without handler) error = %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Deactivate(context.Background(), generation)
	})
	response = httptest.NewRecorder()
	controller.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, RoutePrefix+"pr-workspaces", nil),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unconfigured PR workspaces status = %d, body=%s", response.Code, response.Body.String())
	}
}

type recordingPRWorkspaceHandler struct {
	calls         int
	method        string
	path          string
	rawQuery      string
	body          string
	requestHeader string
}

func (handler *recordingPRWorkspaceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.calls++
	handler.method = r.Method
	handler.path = r.URL.Path
	handler.rawQuery = r.URL.RawQuery
	handler.requestHeader = r.Header.Get("X-Delegation-Test")
	body, _ := io.ReadAll(r.Body)
	handler.body = string(body)
	w.Header().Set("X-PR-Workspaces-Generation", "active")
	w.WriteHeader(http.StatusAccepted)
}
