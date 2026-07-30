package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/netbind"
)

func TestOpenGatewayListeners_HonorsIPv6OnlyHost(t *testing.T) {
	hasIPv4, hasIPv6 := netbind.DetectIPFamilies()
	if !hasIPv6 {
		t.Skip("IPv6 is unavailable in this environment")
	}

	_, result, err := openGatewayListeners("::", 0)
	if err != nil {
		t.Fatalf("openGatewayListeners() error = %v", err)
	}
	startGatewayTestHTTPServer(t, result.Listeners)
	port := mustGatewayAtoi(t, result.Port)

	requireGatewayHTTPReachable(t, "::1", port)
	if hasIPv4 {
		requireGatewayHTTPUnreachable(t, "127.0.0.1", port)
	}
}

func TestOpenGatewayListeners_SupportsExplicitMultiHost(t *testing.T) {
	hasIPv4, hasIPv6 := netbind.DetectIPFamilies()
	if !hasIPv4 || !hasIPv6 {
		t.Skip("dual-stack loopback is unavailable in this environment")
	}

	_, result, err := openGatewayListeners("127.0.0.1,::1", 0)
	if err != nil {
		t.Fatalf("openGatewayListeners() error = %v", err)
	}
	startGatewayTestHTTPServer(t, result.Listeners)
	port := mustGatewayAtoi(t, result.Port)

	requireGatewayHTTPReachable(t, "127.0.0.1", port)
	requireGatewayHTTPReachable(t, "::1", port)
}

func TestGatewayPIDProbeHostUsesOpenedListenerAddress(t *testing.T) {
	loopbackListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("IPv4 loopback is unavailable in this environment: %v", err)
	}
	t.Cleanup(func() {
		_ = loopbackListener.Close()
	})

	got, err := gatewayPIDProbeHost(
		"gateway.invalid",
		[]net.Listener{loopbackListener},
	)
	if err != nil {
		t.Fatalf("gatewayPIDProbeHost() error = %v", err)
	}
	if got != "127.0.0.1" {
		t.Fatalf("gatewayPIDProbeHost() = %q, want 127.0.0.1", got)
	}
}

func TestGatewayPIDProbeHostPreservesSafePlannedHosts(t *testing.T) {
	wildcardListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Skipf("IPv4 wildcard listener is unavailable in this environment: %v", err)
	}
	t.Cleanup(func() {
		_ = wildcardListener.Close()
	})

	tests := map[string]string{"wildcard loopback probe": "127.0.0.1"}
	for name, plannedProbeHost := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := gatewayPIDProbeHost(
				plannedProbeHost,
				[]net.Listener{wildcardListener},
			)
			if err != nil {
				t.Fatalf("gatewayPIDProbeHost() error = %v", err)
			}
			if got != plannedProbeHost {
				t.Fatalf(
					"gatewayPIDProbeHost() = %q, want %q",
					got,
					plannedProbeHost,
				)
			}
		})
	}
}

func TestGatewayPIDProbeHostUsesOpenedLocalhostFamily(t *testing.T) {
	ipv4Occupant, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("IPv4 loopback is unavailable in this environment: %v", err)
	}
	t.Cleanup(func() {
		_ = ipv4Occupant.Close()
	})
	port := ipv4Occupant.Addr().(*net.TCPAddr).Port
	ipv6Gateway, err := net.Listen(
		"tcp6",
		net.JoinHostPort("::1", strconv.Itoa(port)),
	)
	if err != nil {
		t.Skipf("separate IPv6 loopback listener is unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = ipv6Gateway.Close()
	})

	got, err := gatewayPIDProbeHost(
		"localhost",
		[]net.Listener{ipv6Gateway},
	)
	if err != nil {
		t.Fatalf("gatewayPIDProbeHost() error = %v", err)
	}
	if got != "::1" {
		t.Fatalf(
			"gatewayPIDProbeHost() = %q, want opened IPv6 loopback ::1",
			got,
		)
	}
}

func TestOpenGatewayListenersLocalhostFallbackPublishesOpenedFamily(
	t *testing.T,
) {
	hasIPv4, hasIPv6 := netbind.DetectIPFamilies()
	if !hasIPv4 || !hasIPv6 {
		t.Skip("dual-stack loopback is unavailable in this environment")
	}
	ipv4Occupant, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("IPv4 loopback is unavailable in this environment: %v", err)
	}
	t.Cleanup(func() {
		_ = ipv4Occupant.Close()
	})
	port := ipv4Occupant.Addr().(*net.TCPAddr).Port

	plan, result, err := openGatewayListeners("localhost", port)
	if err != nil {
		t.Fatalf("openGatewayListeners(localhost fallback) error = %v", err)
	}
	startGatewayTestHTTPServer(t, result.Listeners)
	probeHost, err := gatewayPIDProbeHost(
		plan.ProbeHost,
		result.Listeners,
	)
	if err != nil {
		t.Fatalf("gatewayPIDProbeHost() error = %v", err)
	}
	if probeHost != "::1" {
		t.Fatalf(
			"gateway PID probe host = %q, want opened IPv6 loopback ::1",
			probeHost,
		)
	}
	requireGatewayHTTPReachable(t, probeHost, port)
}

func TestGatewayPIDProbeHostMapsOpenedWildcardToNumericLoopback(
	t *testing.T,
) {
	plan, result, err := openGatewayListeners("*", 0)
	if err != nil {
		t.Fatalf("openGatewayListeners(*) error = %v", err)
	}
	startGatewayTestHTTPServer(t, result.Listeners)
	probeHost, err := gatewayPIDProbeHost(
		plan.ProbeHost,
		result.Listeners,
	)
	if err != nil {
		t.Fatalf("gatewayPIDProbeHost() error = %v", err)
	}
	probeIP := net.ParseIP(probeHost)
	if probeIP == nil || !probeIP.IsLoopback() {
		t.Fatalf("gateway PID probe host = %q, want numeric loopback", probeHost)
	}
	requireGatewayHTTPReachable(
		t,
		probeHost,
		mustGatewayAtoi(t, result.Port),
	)
}

func startGatewayTestHTTPServer(t *testing.T, listeners []net.Listener) {
	t.Helper()

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}),
	}

	errCh := make(chan error, len(listeners))
	for _, listener := range listeners {
		ln := listener
		go func() {
			errCh <- server.Serve(ln)
		}()
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		for range listeners {
			err := <-errCh
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("server.Serve() error = %v", err)
			}
		}
	})
}

func requireGatewayHTTPReachable(t *testing.T, host string, port int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := gatewayHTTPGet(host, port)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %s:%d to be reachable: %v", host, port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func requireGatewayHTTPUnreachable(t *testing.T, host string, port int) {
	t.Helper()
	if err := gatewayHTTPGet(host, port); err == nil {
		t.Fatalf("expected %s:%d to be unreachable", host, port)
	}
}

func gatewayHTTPGet(host string, port int) error {
	client := &http.Client{
		Timeout: 300 * time.Millisecond,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}

	resp, err := client.Get("http://" + net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return nil
}

func mustGatewayAtoi(t *testing.T, value string) int {
	t.Helper()
	n, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("Atoi(%q) error = %v", value, err)
	}
	return n
}
