package operator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControllerDelegatesProtectedPRDevelopmentSubtreeToActiveGeneration(
	t *testing.T,
) {
	handler := &recordingPRDevelopmentHandler{}
	backend, err := NewBackend(BackendConfig{
		Store:         &fakeStore{},
		PRDevelopment: handler,
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

	target := RoutePrefix +
		"pr-development/pdc_11111111111111111111111111111111?trace=kept"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("X-Request-Test", "preserved")
	response := httptest.NewRecorder()

	controller.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if handler.calls != 1 ||
		handler.method != http.MethodGet ||
		handler.path != RoutePrefix+
			"pr-development/pdc_11111111111111111111111111111111" ||
		handler.rawQuery != "trace=kept" ||
		handler.requestHeader != "preserved" {
		t.Fatalf("delegated request = %#v", handler)
	}
}

func TestControllerRejectsInvalidOrUnconfiguredPRDevelopmentRoutes(t *testing.T) {
	handler := &recordingPRDevelopmentHandler{}
	backend, err := NewBackend(BackendConfig{
		Store:         &fakeStore{},
		PRDevelopment: handler,
	})
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	controller := NewController()
	generation, err := controller.Activate(backend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		RoutePrefix+"pr-development%2fpdc_11111111111111111111111111111111",
		nil,
	)
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || handler.calls != 0 {
		t.Fatalf("encoded route = %d, calls=%d", response.Code, handler.calls)
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
		httptest.NewRequest(http.MethodGet, RoutePrefix+"pr-development", nil),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unconfigured status = %d", response.Code)
	}
}

type recordingPRDevelopmentHandler struct {
	calls         int
	method        string
	path          string
	rawQuery      string
	requestHeader string
}

func (handler *recordingPRDevelopmentHandler) ServeHTTP(
	w http.ResponseWriter,
	request *http.Request,
) {
	handler.calls++
	handler.method = request.Method
	handler.path = request.URL.Path
	handler.rawQuery = request.URL.RawQuery
	handler.requestHeader = request.Header.Get("X-Request-Test")
	w.WriteHeader(http.StatusAccepted)
}
