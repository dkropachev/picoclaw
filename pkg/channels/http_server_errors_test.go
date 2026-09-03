package channels

import (
	"errors"
	"testing"
)

func TestManagerHTTPServerErrorsAreBoundedAndNonblocking(t *testing.T) {
	var absent *Manager
	if absent.HTTPServerErrors() != nil {
		t.Fatal("nil manager exposed an HTTP server error channel")
	}

	manager := &Manager{httpServerErrors: make(chan error, 1)}
	manager.reportHTTPServerError(nil)
	want := errors.New("listener failed")
	manager.reportHTTPServerError(want)
	manager.reportHTTPServerError(errors.New("overflow is dropped"))
	select {
	case got := <-manager.HTTPServerErrors():
		if !errors.Is(got, want) {
			t.Fatalf("HTTP server error = %v, want %v", got, want)
		}
	default:
		t.Fatal("HTTP server error was not reported")
	}
}
