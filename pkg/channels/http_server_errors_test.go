package channels

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

type terminalHTTPListener struct {
	accept func() (net.Conn, error)
	closed atomic.Bool
}

func (listener *terminalHTTPListener) Accept() (net.Conn, error) {
	return listener.accept()
}

func (listener *terminalHTTPListener) Close() error {
	listener.closed.Store(true)
	return nil
}

func (*terminalHTTPListener) Addr() net.Addr {
	return terminalHTTPAddr("terminal-listener")
}

type terminalHTTPAddr string

func (terminalHTTPAddr) Network() string { return "test" }
func (addr terminalHTTPAddr) String() string {
	return string(addr)
}

func newHTTPServerErrorTestManager(t *testing.T) *Manager {
	t.Helper()
	messageBus := bus.NewMessageBus()
	manager, err := NewManager(config.DefaultConfig(), messageBus, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := manager.StopAll(ctx); err != nil {
			t.Errorf("StopAll() cleanup error = %v", err)
		}
		messageBus.Close()
	})
	return manager
}

func waitForHTTPServerError(t *testing.T, manager *Manager) error {
	t.Helper()
	select {
	case err := <-manager.HTTPServerErrors():
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shared HTTP server failure")
		return nil
	}
}

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

func TestManagerReportsPreopenedHTTPListenerFailure(t *testing.T) {
	manager := newHTTPServerErrorTestManager(t)
	want := errors.New("accept failed")
	listener := &terminalHTTPListener{
		accept: func() (net.Conn, error) { return nil, want },
	}
	manager.SetupHTTPServerListeners([]net.Listener{listener}, "unused", nil)
	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	got := waitForHTTPServerError(t, manager)
	if !errors.Is(got, want) || !strings.Contains(got.Error(), "terminal-listener") {
		t.Fatalf("HTTP server error = %v, want wrapped accept failure with listener address", got)
	}
	if !listener.closed.Load() {
		t.Fatal("http.Server.Serve did not close the failed listener")
	}
}

func TestManagerReportsPreopenedHTTPListenerPanic(t *testing.T) {
	manager := newHTTPServerErrorTestManager(t)
	listener := &terminalHTTPListener{
		accept: func() (net.Conn, error) { panic("listener panic must not escape") },
	}
	manager.SetupHTTPServerListeners([]net.Listener{listener}, "unused", nil)
	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	got := waitForHTTPServerError(t, manager)
	if got == nil || got.Error() != "shared HTTP server goroutine panicked" {
		t.Fatalf("HTTP server panic error = %v", got)
	}
}

func TestManagerReportsAddressBasedHTTPListenFailure(t *testing.T) {
	manager := newHTTPServerErrorTestManager(t)
	manager.SetupHTTPServer("127.0.0.1:-1", nil)
	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	got := waitForHTTPServerError(t, manager)
	if got == nil || !strings.Contains(got.Error(), "serve shared HTTP listener") {
		t.Fatalf("HTTP server listen error = %v", got)
	}
}

func TestManagerReportsAddressBasedHTTPServerPanic(t *testing.T) {
	manager := newHTTPServerErrorTestManager(t)
	manager.SetupHTTPServer("127.0.0.1:0", nil)
	manager.httpServer.BaseContext = func(net.Listener) context.Context {
		panic("base-context panic must not escape")
	}
	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	got := waitForHTTPServerError(t, manager)
	if got == nil || got.Error() != "shared HTTP server goroutine panicked" {
		t.Fatalf("HTTP server panic error = %v", got)
	}
}
