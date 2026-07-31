package operator

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControllerDelegatesProtectedReviewSubtreeToActiveGeneration(t *testing.T) {
	reviews := &recordingReviewsHandler{}
	backend, err := NewBackend(BackendConfig{
		Store:   &fakeStore{},
		Reviews: reviews,
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
		"reviews/prc_11111111111111111111111111111111/submit?trace=kept"
	request := httptest.NewRequest(
		http.MethodPost,
		target,
		strings.NewReader(`{"expected_version":7}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-Test", "preserved")
	response := httptest.NewRecorder()

	controller.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-Reviews-Generation"); got != "active" {
		t.Fatalf("delegated response header = %q, want active", got)
	}
	if got := response.Body.String(); got != `{"delegated":true}` {
		t.Fatalf("delegated response body = %q", got)
	}
	if reviews.calls != 1 ||
		reviews.method != http.MethodPost ||
		reviews.path != RoutePrefix+
			"reviews/prc_11111111111111111111111111111111/submit" ||
		reviews.rawQuery != "trace=kept" ||
		reviews.body != `{"expected_version":7}` ||
		reviews.requestHeader != "preserved" {
		t.Fatalf("delegated request = %#v", reviews)
	}
}

func TestControllerDoesNotDelegateInvalidOrUnconfiguredReviewRoutes(t *testing.T) {
	reviews := &recordingReviewsHandler{}
	backend, err := NewBackend(BackendConfig{
		Store:   &fakeStore{},
		Reviews: reviews,
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
		RoutePrefix+"reviews%2fprc_11111111111111111111111111111111",
		nil,
	)
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("encoded-path status = %d, body=%s", response.Code, response.Body.String())
	}
	if reviews.calls != 0 {
		t.Fatalf("invalid encoded path delegated %d times", reviews.calls)
	}
	if err := controller.Deactivate(context.Background(), generation); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}

	withoutReviews, err := NewBackend(BackendConfig{Store: &fakeStore{}})
	if err != nil {
		t.Fatalf("NewBackend(without reviews) error = %v", err)
	}
	generation, err = controller.Activate(withoutReviews)
	if err != nil {
		t.Fatalf("Activate(without reviews) error = %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Deactivate(context.Background(), generation)
	})
	response = httptest.NewRecorder()
	controller.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, RoutePrefix+"reviews", nil),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unconfigured reviews status = %d, body=%s", response.Code, response.Body.String())
	}
}

type recordingReviewsHandler struct {
	calls         int
	method        string
	path          string
	rawQuery      string
	body          string
	requestHeader string
}

func (handler *recordingReviewsHandler) ServeHTTP(
	w http.ResponseWriter,
	request *http.Request,
) {
	handler.calls++
	handler.method = request.Method
	handler.path = request.URL.Path
	handler.rawQuery = request.URL.RawQuery
	handler.requestHeader = request.Header.Get("X-Request-Test")
	body, _ := io.ReadAll(request.Body)
	handler.body = string(body)
	w.Header().Set("X-Reviews-Generation", "active")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"delegated":true}`))
}
