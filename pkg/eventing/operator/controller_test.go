package operator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type blockingResponseWriter struct {
	header       http.Header
	status       int
	body         []byte
	writeEntered chan struct{}
	writeRelease chan struct{}
	enteredOnce  sync.Once
	releaseOnce  sync.Once
}

func newBlockingResponseWriter() *blockingResponseWriter {
	return &blockingResponseWriter{
		header:       make(http.Header),
		writeEntered: make(chan struct{}),
		writeRelease: make(chan struct{}),
	}
}

func (writer *blockingResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *blockingResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}

func (writer *blockingResponseWriter) Write(body []byte) (int, error) {
	writer.enteredOnce.Do(func() {
		close(writer.writeEntered)
	})
	<-writer.writeRelease
	writer.body = append(writer.body, body...)
	return len(body), nil
}

func (writer *blockingResponseWriter) unblock() {
	writer.releaseOnce.Do(func() {
		close(writer.writeRelease)
	})
}

func TestControllerStageIsNonAcceptingUntilCommitAndAbortIsSafe(t *testing.T) {
	store := &fakeStore{}
	backend := testBackend(t, store)
	controller := NewController()

	staged, err := controller.Stage(backend)
	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	generation := staged.Generation()
	if generation == (Generation{}) {
		t.Fatal("staged enabled generation is zero")
	}
	if controller.IsActive(generation) {
		t.Fatal("staged generation is active before commit")
	}
	if _, err = controller.Stage(backend); !errors.Is(err, ErrActivationStaged) {
		t.Fatalf("second Stage() error = %v", err)
	}
	if err = controller.Deactivate(
		context.Background(),
		Generation{},
	); !errors.Is(err, ErrActivationStaged) {
		t.Fatalf("Deactivate() while staged error = %v", err)
	}
	beforeCommit := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events",
		"",
		false,
	)
	if beforeCommit.Code != http.StatusServiceUnavailable {
		t.Fatalf("before commit status = %d", beforeCommit.Code)
	}

	staged.Commit()
	if !controller.IsActive(generation) {
		t.Fatal("committed generation is not active")
	}
	staged.Abort()
	if !controller.IsActive(generation) {
		t.Fatal("stale Abort affected committed generation")
	}
	afterCommit := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events",
		"",
		false,
	)
	if afterCommit.Code != http.StatusOK {
		t.Fatalf("after commit status = %d, body=%s", afterCommit.Code, afterCommit.Body.String())
	}
	if err = controller.Deactivate(context.Background(), generation); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}

	aborted, err := controller.Stage(backend)
	if err != nil {
		t.Fatalf("Stage(aborted) error = %v", err)
	}
	aborted.Abort()
	if _, err = controller.Activate(backend); err != nil {
		t.Fatalf("Activate(after abort) error = %v", err)
	}
	active := controller.activeGenerationForTest()
	if err = controller.Deactivate(context.Background(), active); err != nil {
		t.Fatalf("Deactivate(after abort) error = %v", err)
	}

	disabled, err := controller.Stage(nil)
	if err != nil {
		t.Fatalf("Stage(nil) error = %v", err)
	}
	if disabled.Generation() != (Generation{}) {
		t.Fatal("disabled generation is nonzero")
	}
	disabled.Commit()
	inactive := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events",
		"",
		false,
	)
	if inactive.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status = %d", inactive.Code)
	}
}

func TestControllerDeactivateDoesNotWaitForNetworkWrite(t *testing.T) {
	backend := testBackend(t, &fakeStore{})
	controller := NewController()
	generation, err := controller.Activate(backend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}

	writer := newBlockingResponseWriter()
	t.Cleanup(writer.unblock)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		controller.ServeHTTP(
			writer,
			httptest.NewRequest(
				http.MethodGet,
				RoutePrefix+"events",
				nil,
			),
		)
	}()

	select {
	case <-writer.writeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("response did not reach the blocked network write")
	}

	deactivateDone := make(chan error, 1)
	go func() {
		deactivateDone <- controller.Deactivate(context.Background(), generation)
	}()
	select {
	case err = <-deactivateDone:
		if err != nil {
			t.Fatalf("Deactivate() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Deactivate() waited for a detached network write")
	}

	select {
	case <-requestDone:
		t.Fatal("request completed while its network write was blocked")
	default:
	}

	writer.unblock()
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not complete after unblocking the network write")
	}
	if writer.status != http.StatusOK {
		t.Fatalf("response status = %d, want %d", writer.status, http.StatusOK)
	}
	if len(writer.body) == 0 {
		t.Fatal("response body is empty")
	}
}

func TestControllerGenerationDrainFencesReplacementAndStaleCleanup(t *testing.T) {
	store := &fakeStore{
		listEntered: make(chan struct{}),
		listRelease: make(chan struct{}),
	}
	backend := testBackend(t, store)
	controller := NewController()
	first, err := controller.Activate(backend)
	if err != nil {
		t.Fatalf("Activate(first) error = %v", err)
	}

	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseDone <- performRequest(
			controller,
			http.MethodGet,
			RoutePrefix+"events",
			"",
			false,
		)
	}()
	select {
	case <-store.listEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("list operation was not admitted")
	}

	timeoutContext, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err = controller.Deactivate(timeoutContext, first); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatalf("Deactivate(timeout) error = %v", err)
	}
	if controller.IsActive(first) {
		t.Fatal("retiring generation remains active")
	}
	rejected := performRequest(
		controller,
		http.MethodGet,
		RoutePrefix+"events",
		"",
		false,
	)
	if rejected.Code != http.StatusServiceUnavailable ||
		rejected.Header().Get("Retry-After") != "1" {
		t.Fatalf(
			"request during drain status=%d Retry-After=%q",
			rejected.Code,
			rejected.Header().Get("Retry-After"),
		)
	}
	if _, err = controller.Activate(backend); !errors.Is(err, ErrGenerationDraining) {
		t.Fatalf("Activate(during drain) error = %v", err)
	}

	close(store.listRelease)
	select {
	case response := <-responseDone:
		if response.Code != http.StatusOK {
			t.Fatalf("admitted response status = %d, body=%s", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admitted response did not drain")
	}
	if err = controller.Deactivate(context.Background(), first); err != nil {
		t.Fatalf("Deactivate(drained first) error = %v", err)
	}

	second, err := controller.Activate(backend)
	if err != nil {
		t.Fatalf("Activate(second) error = %v", err)
	}
	if err = controller.Deactivate(context.Background(), first); err != nil {
		t.Fatalf("stale first Deactivate() error = %v", err)
	}
	if !controller.IsActive(second) {
		t.Fatal("stale cleanup deactivated replacement")
	}

	foreign := NewController()
	if err = foreign.Deactivate(context.Background(), second); !errors.Is(
		err,
		ErrGenerationNotOwned,
	) {
		t.Fatalf("foreign Deactivate() error = %v", err)
	}
	if err = controller.Deactivate(context.Background(), second); err != nil {
		t.Fatalf("Deactivate(second) error = %v", err)
	}
}

func TestControllerRejectsActiveReplacementAndForeignGeneration(t *testing.T) {
	backend := testBackend(t, &fakeStore{})
	first := NewController()
	second := NewController()
	generation, err := first.Activate(backend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if _, err = first.Activate(backend); !errors.Is(err, ErrActiveGeneration) {
		t.Fatalf("second Activate() error = %v", err)
	}
	if err = second.Deactivate(
		context.Background(),
		generation,
	); !errors.Is(err, ErrGenerationNotOwned) {
		t.Fatalf("foreign Deactivate() error = %v", err)
	}
	if err = first.Deactivate(context.Background(), generation); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}
	if err = first.Deactivate(context.Background(), generation); err != nil {
		t.Fatalf("repeated Deactivate() error = %v", err)
	}
}

func TestControllerZeroValuesAreSafe(t *testing.T) {
	controller := NewController()
	if err := controller.Deactivate(
		context.Background(),
		Generation{},
	); err != nil {
		t.Fatalf("Deactivate(zero) error = %v", err)
	}
	var staged *StagedGeneration
	staged.Commit()
	staged.Abort()
	if _, err := controller.Activate(nil); err == nil {
		t.Fatal("Activate(nil) error = nil")
	}
}

func (controller *Controller) activeGenerationForTest() Generation {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return Generation{controller: controller, state: controller.active}
}
