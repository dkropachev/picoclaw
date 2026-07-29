package channels

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDynamicServeMuxExactMatch(t *testing.T) {
	dm := newDynamicServeMux()
	dm.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestDynamicServeMuxSubtreePrefixMatch(t *testing.T) {
	dm := newDynamicServeMux()
	dm.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	for _, path := range []string{"/api/", "/api/v1", "/api/v1/resource"} {
		rec := httptest.NewRecorder()
		dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusCreated {
			t.Fatalf("path %q: expected 201, got %d", path, rec.Code)
		}
	}
}

func TestDynamicServeMuxExactOverPrefix(t *testing.T) {
	dm := newDynamicServeMux()
	dm.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	dm.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	// Exact match wins
	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("exact match: expected 200, got %d", rec.Code)
	}

	// Prefix match for sub-paths
	rec = httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("prefix match: expected 201, got %d", rec.Code)
	}
}

func TestDynamicServeMuxLongestPrefixWins(t *testing.T) {
	dm := newDynamicServeMux()
	dm.HandleFunc("/a/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	dm.HandleFunc("/a/b/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/b/c", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("longest prefix: expected 202, got %d", rec.Code)
	}
}

func TestDynamicServeMuxNotFound(t *testing.T) {
	dm := newDynamicServeMux()
	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nonexistent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDynamicServeMuxUnhandle(t *testing.T) {
	dm := newDynamicServeMux()
	dm.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Verify it works before removal
	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("before unhandle: expected 200, got %d", rec.Code)
	}

	// Remove and verify 404
	dm.Unhandle("/test")
	rec = httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("after unhandle: expected 404, got %d", rec.Code)
	}
}

func TestDynamicServeMuxConcurrent(t *testing.T) {
	dm := newDynamicServeMux()
	dm.HandleFunc("/static", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var wg sync.WaitGroup
	const goroutines = 50

	// Concurrent Handle/Unhandle
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pattern := "/concurrent"
			if i%2 == 0 {
				dm.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusAccepted)
				})
			} else {
				dm.Unhandle(pattern)
			}
		}(i)
	}

	// Concurrent ServeHTTP
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static", nil))
			// Should not panic; result is either 200 or 404
			_ = rec.Code
		}()
	}

	wg.Wait()
}

func TestDynamicServeMuxHandleUsesHandler(t *testing.T) {
	dm := newDynamicServeMux()

	var called bool
	dm.Handle("/handler", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/handler", nil))
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestDynamicServeMuxRegisterOwnedRejectsInvalidAndConflictingRoutes(t *testing.T) {
	dm := newDynamicServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, pattern := range []string{"", "relative", "/events?source=test", "/events#fragment"} {
		if release, err := dm.registerOwned(pattern, handler); err == nil || release != nil {
			t.Fatalf("registerOwned(%q) = (%p, %v), want nil release and error", pattern, release, err)
		}
	}
	if release, err := dm.registerOwned("/nil", nil); err == nil || release != nil {
		t.Fatalf("registerOwned(nil handler) = (%p, %v), want nil release and error", release, err)
	}

	dm.Handle("/health", handler)
	if release, err := dm.registerOwned("/health", handler); !errors.Is(err, ErrHTTPRouteConflict) ||
		release != nil {
		t.Fatalf(
			"duplicate registerOwned() = (%p, %v), want ErrHTTPRouteConflict",
			release,
			err,
		)
	}

	dm.Handle("/webhooks/", handler)
	for _, pattern := range []string{"/webhooks/events", "/webhooks/events/"} {
		if release, err := dm.registerOwned(pattern, handler); !errors.Is(err, ErrHTTPRouteConflict) ||
			release != nil {
			t.Fatalf(
				"overlapping registerOwned(%q) = (%p, %v), want ErrHTTPRouteConflict",
				pattern,
				release,
				err,
			)
		}
	}

	exactFirst := newDynamicServeMux()
	exactFirst.Handle("/reserved/exact", handler)
	if release, err := exactFirst.registerOwned(
		"/reserved/",
		handler,
	); !errors.Is(err, ErrHTTPRouteConflict) || release != nil {
		t.Fatalf(
			"prefix over exact registration = (%p, %v), want ErrHTTPRouteConflict",
			release,
			err,
		)
	}

	// The exact path /api and the subtree /api/ match disjoint requests.
	dm.Handle("/api/", handler)
	release, err := dm.registerOwned("/api", handler)
	if err != nil {
		t.Fatalf("registerOwned(disjoint exact route) error = %v", err)
	}
	release()
}

func TestDynamicServeMuxOwnedReleaseIsIdentitySafeAndIdempotent(t *testing.T) {
	dm := newDynamicServeMux()
	first := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	second := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	releaseFirst, err := dm.registerOwned("/events", first)
	if err != nil {
		t.Fatalf("register first route: %v", err)
	}
	releaseFirst()

	releaseSecond, err := dm.registerOwned("/events", second)
	if err != nil {
		t.Fatalf("register replacement route: %v", err)
	}
	defer releaseSecond()

	// A stale, repeated release from the first owner must not remove the second.
	releaseFirst()
	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status after stale release = %d, want %d", rec.Code, http.StatusAccepted)
	}
}

func TestDynamicServeMuxLegacyRoutesCannotShadowOrRemoveOwnedRoute(t *testing.T) {
	dm := newDynamicServeMux()
	owned := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	legacy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	release, err := dm.registerOwned("/webhooks/events/", owned)
	if err != nil {
		t.Fatalf("registerOwned() error = %v", err)
	}

	for _, pattern := range []string{
		"/webhooks/events/",
		"/webhooks/events/build-system",
		"/webhooks/",
	} {
		dm.Handle(pattern, legacy)
	}
	dm.Unhandle("/webhooks/events/")

	response := httptest.NewRecorder()
	dm.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/webhooks/events/build-system", nil),
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf(
			"owned route after legacy collision = %d, want %d",
			response.Code,
			http.StatusAccepted,
		)
	}

	release()
	response = httptest.NewRecorder()
	dm.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/webhooks/events/build-system", nil),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("released owned route = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestDynamicServeMuxServeDoesNotHoldRouteLockDuringHandler(t *testing.T) {
	dm := newDynamicServeMux()
	started := make(chan struct{})
	unblock := make(chan struct{})
	release, err := dm.registerOwned("/blocking", http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-unblock
			w.WriteHeader(http.StatusOK)
		},
	))
	if err != nil {
		t.Fatalf("register blocking route: %v", err)
	}

	served := make(chan struct{})
	go func() {
		defer close(served)
		rec := httptest.NewRecorder()
		dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/blocking", nil))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(unblock)
		t.Fatal("downstream handler did not start")
	}

	released := make(chan struct{})
	go func() {
		release()
		close(released)
	}()
	select {
	case <-released:
	case <-time.After(time.Second):
		close(unblock)
		t.Fatal("route release blocked behind downstream handler")
	}

	close(unblock)
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("downstream handler did not finish")
	}

	rec := httptest.NewRecorder()
	dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/blocking", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status after release = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDynamicServeMuxConcurrentOwnedRegistrationAndServing(t *testing.T) {
	dm := newDynamicServeMux()
	const goroutines = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			pattern := fmt.Sprintf("/owned/%d", index)
			release, err := dm.registerOwned(pattern, http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				},
			))
			if err != nil {
				t.Errorf("registerOwned(%q) error = %v", pattern, err)
				return
			}
			rec := httptest.NewRecorder()
			dm.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pattern, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("ServeHTTP(%q) status = %d, want %d", pattern, rec.Code, http.StatusOK)
			}
			release()
			release()
		}(i)
	}
	wg.Wait()
}
