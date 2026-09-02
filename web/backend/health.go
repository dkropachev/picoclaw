package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/sipeed/picoclaw/pkg/health"
)

const (
	launcherHealthPath = "/health"
	launcherReadyPath  = "/ready"
)

func newLauncherServeMux(startedAt time.Time) *http.ServeMux {
	mux := http.NewServeMux()
	registerLauncherHealthRoutes(mux, startedAt)
	return mux
}

// registerLauncherHealthRoutes exposes health for the launcher itself. Gateway
// state is intentionally excluded because the gateway is an optional,
// launcher-managed child process.
func registerLauncherHealthRoutes(mux *http.ServeMux, startedAt time.Time) {
	mux.HandleFunc("GET "+launcherHealthPath, launcherStatusHandler("ok", startedAt))
	mux.HandleFunc("GET "+launcherReadyPath, launcherStatusHandler("ready", startedAt))
}

func launcherStatusHandler(status string, startedAt time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}

		uptime := time.Since(startedAt)
		if uptime < 0 {
			uptime = 0
		}
		_ = json.NewEncoder(w).Encode(health.StatusResponse{
			Status: status,
			Uptime: uptime.String(),
			PID:    os.Getpid(),
		})
	}
}

// allowLoopbackLauncherHealth lets the container-local health command reach
// launcher probes even when an operator disables the general localhost CIDR
// bypass. Non-health traffic and non-loopback probes retain network policy.
func allowLoopbackLauncherHealth(healthRoutes, networkControlled http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLauncherHealthRequest(r) && isLoopbackHTTPPeer(r.RemoteAddr) {
			healthRoutes.ServeHTTP(w, r)
			return
		}
		networkControlled.ServeHTTP(w, r)
	})
}

func isLauncherHealthRequest(r *http.Request) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	return r.URL.Path == launcherHealthPath || r.URL.Path == launcherReadyPath
}

func isLoopbackHTTPPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
