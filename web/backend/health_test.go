package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

func TestLauncherHealthRoutesWithoutGateway(t *testing.T) {
	mux := newLauncherServeMux(time.Now().Add(-time.Second))

	for _, test := range []struct {
		name       string
		method     string
		path       string
		wantStatus string
	}{
		{name: "liveness", method: http.MethodGet, path: launcherHealthPath, wantStatus: "ok"},
		{name: "readiness", method: http.MethodGet, path: launcherReadyPath, wantStatus: "ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}

			var response health.StatusResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Status != test.wantStatus {
				t.Fatalf("response status = %q, want %q", response.Status, test.wantStatus)
			}
			if response.Uptime == "" {
				t.Fatal("response uptime is empty")
			}
			if response.PID != os.Getpid() {
				t.Fatalf("response PID = %d, want %d", response.PID, os.Getpid())
			}
		})
	}
}

func TestLauncherHealthRoutesAllowHeadWithoutBody(t *testing.T) {
	mux := http.NewServeMux()
	registerLauncherHealthRoutes(mux, time.Now())

	for _, path := range []string{launcherHealthPath, launcherReadyPath} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, path, nil))

		if recorder.Code != http.StatusOK {
			t.Fatalf("HEAD %s status = %d, want %d", path, recorder.Code, http.StatusOK)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("HEAD %s body = %q, want empty", path, recorder.Body.String())
		}
	}
}

func TestLauncherHealthClampsNegativeUptime(t *testing.T) {
	mux := http.NewServeMux()
	registerLauncherHealthRoutes(mux, time.Now().Add(time.Hour))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, launcherHealthPath, nil))

	var response health.StatusResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Uptime != "0s" {
		t.Fatalf("uptime = %q, want 0s", response.Uptime)
	}
}

func TestLauncherHealthRoutesRejectUnsupportedMethods(t *testing.T) {
	mux := http.NewServeMux()
	registerLauncherHealthRoutes(mux, time.Now())

	for _, path := range []string{launcherHealthPath, launcherReadyPath} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))

		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want %d", path, recorder.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestLoopbackLauncherHealthBypassesRestrictiveCIDRPolicy(t *testing.T) {
	mux := http.NewServeMux()
	registerLauncherHealthRoutes(mux, time.Now())

	restricted, err := middleware.IPAllowlist(middleware.IPAllowlistConfig{
		AllowedCIDRs:         []string{"192.168.1.0/24"},
		AllowLocalhostBypass: false,
	}, mux)
	if err != nil {
		t.Fatalf("IPAllowlist() error = %v", err)
	}
	handler := middleware.LauncherDashboardAuth(
		middleware.LauncherDashboardAuthConfig{ExpectedCookie: "test-session"},
		allowLoopbackLauncherHealth(mux, restricted),
	)

	for _, test := range []struct {
		name       string
		method     string
		path       string
		remoteAddr string
		auth       bool
		wantCode   int
	}{
		{
			name: "container IPv4 liveness", method: http.MethodGet,
			path: launcherHealthPath, remoteAddr: "127.0.0.1:41000", wantCode: http.StatusOK,
		},
		{
			name: "container IPv6 readiness", method: http.MethodHead,
			path: launcherReadyPath, remoteAddr: "[::1]:41000", wantCode: http.StatusOK,
		},
		{
			name: "remote probe retains CIDR policy", method: http.MethodGet,
			path: launcherHealthPath, remoteAddr: "203.0.113.10:41000", wantCode: http.StatusForbidden,
		},
		{
			name: "non-health path retains CIDR policy", method: http.MethodGet,
			path: "/", remoteAddr: "127.0.0.1:41000", auth: true, wantCode: http.StatusForbidden,
		},
		{
			name: "health mutation retains dashboard auth", method: http.MethodPost,
			path: launcherHealthPath, remoteAddr: "127.0.0.1:41000", wantCode: http.StatusFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			request.RemoteAddr = test.remoteAddr
			if test.auth {
				request.AddCookie(&http.Cookie{
					Name:  middleware.LauncherDashboardCookieName,
					Value: "test-session",
				})
			}
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantCode {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantCode)
			}
		})
	}
}

func TestLauncherHealthRequestClassification(t *testing.T) {
	if isLauncherHealthRequest(nil) {
		t.Fatal("nil request classified as health")
	}
	for _, test := range []struct {
		method string
		path   string
		want   bool
	}{
		{method: http.MethodGet, path: launcherHealthPath, want: true},
		{method: http.MethodHead, path: launcherReadyPath, want: true},
		{method: http.MethodPost, path: launcherHealthPath, want: false},
		{method: http.MethodGet, path: "/other", want: false},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		if got := isLauncherHealthRequest(request); got != test.want {
			t.Fatalf("isLauncherHealthRequest(%s %s) = %t, want %t", test.method, test.path, got, test.want)
		}
	}
}

func TestLoopbackHTTPPeerClassification(t *testing.T) {
	for _, test := range []struct {
		remoteAddr string
		want       bool
	}{
		{remoteAddr: "127.0.0.1:1234", want: true},
		{remoteAddr: "[::1]:1234", want: true},
		{remoteAddr: "203.0.113.10:1234", want: false},
		{remoteAddr: "not-an-ip:1234", want: false},
		{remoteAddr: "missing-port", want: false},
	} {
		if got := isLoopbackHTTPPeer(test.remoteAddr); got != test.want {
			t.Fatalf("isLoopbackHTTPPeer(%q) = %t, want %t", test.remoteAddr, got, test.want)
		}
	}
}
