package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLauncherAutoStartCanonicalLifecycleCoverage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux autostart contract")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	handler := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	handler.debug = true
	t.Cleanup(handler.Shutdown)

	executable, args, err := handler.resolveLaunchCommand()
	if err != nil {
		t.Fatal(err)
	}
	if executable == "" || len(args) != 3 || args[0] != "-no-browser" || args[1] != "-d" {
		t.Fatalf("launch command executable=%q args=%#v", executable, args)
	}

	malformed := httptest.NewRecorder()
	handler.handleSetAutoStart(
		malformed,
		httptest.NewRequest(http.MethodPut, "/api/system/autostart", strings.NewReader("{")),
	)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed request status=%d body=%s", malformed.Code, malformed.Body.String())
	}

	enable := httptest.NewRecorder()
	handler.handleSetAutoStart(
		enable,
		httptest.NewRequest(
			http.MethodPut,
			"/api/system/autostart",
			strings.NewReader(`{"enabled":true}`),
		),
	)
	if enable.Code != http.StatusOK || !strings.Contains(enable.Body.String(), `"enabled":true`) {
		t.Fatalf("enable status=%d body=%s", enable.Code, enable.Body.String())
	}
	desktopPath := filepath.Join(home, ".config", "autostart", "picoclaw-web.desktop")
	desktop, err := os.ReadFile(desktopPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(desktop), "Terminal=false") ||
		!strings.Contains(string(desktop), "Exec=") {
		t.Fatalf("desktop entry=%s", desktop)
	}

	get := httptest.NewRecorder()
	handler.handleGetAutoStart(
		get,
		httptest.NewRequest(http.MethodGet, "/api/system/autostart", nil),
	)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"enabled":true`) {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}

	disable := httptest.NewRecorder()
	handler.handleSetAutoStart(
		disable,
		httptest.NewRequest(
			http.MethodPut,
			"/api/system/autostart",
			strings.NewReader(`{"enabled":false}`),
		),
	)
	if disable.Code != http.StatusOK || !strings.Contains(disable.Body.String(), `"enabled":false`) {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	if _, err := os.Stat(desktopPath); !os.IsNotExist(err) {
		t.Fatalf("desktop entry survived disable: %v", err)
	}

	if err := setLinuxAutoStart(false, executable, args); err != nil {
		t.Fatalf("idempotent disable: %v", err)
	}
	if got := shellQuote(""); got != "''" {
		t.Fatalf("empty shell quote=%q", got)
	}
	if got := buildLinuxExecLine("/tmp/pico claw", []string{"plain", "a'b"}); !strings.Contains(got, `'"'"'`) {
		t.Fatalf("quoted exec line=%q", got)
	}
}
