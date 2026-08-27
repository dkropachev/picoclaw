package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// gatewayRuntimeHarness owns every application path used by one gateway test.
// It intentionally uses environment overrides because existing gateway PID and
// auth helpers resolve PicoClaw home globally. Tests using this harness must not
// call t.Parallel until those production dependencies are handler-scoped.
type gatewayRuntimeHarness struct {
	Root      string
	OSHome    string
	PicoHome  string
	Config    string
	Workspace string
	EventDB   string
	Binary    string
	Handler   *Handler
}

func newGatewayRuntimeHarness(
	t *testing.T,
	configure func(*config.Config),
) *gatewayRuntimeHarness {
	t.Helper()
	if apiSuiteTestRuntime == nil {
		t.Fatal("API suite test runtime is not initialized")
	}

	root := t.TempDir()
	harness := &gatewayRuntimeHarness{
		Root:      root,
		OSHome:    filepath.Join(root, "os-home"),
		PicoHome:  filepath.Join(root, "picoclaw-home"),
		Workspace: filepath.Join(root, "picoclaw-home", "workspace"),
		Binary:    apiSuiteTestRuntime.FixtureBinary,
	}
	harness.Config = filepath.Join(harness.PicoHome, "config.json")
	harness.EventDB = filepath.Join(harness.Workspace, "eventing", "events.db")

	for _, dir := range []string{
		harness.OSHome,
		filepath.Join(harness.OSHome, ".config"),
		filepath.Join(harness.OSHome, ".local", "share"),
		filepath.Join(harness.OSHome, ".local", "state"),
		filepath.Join(harness.OSHome, ".cache"),
		filepath.Join(harness.OSHome, ".run"),
		filepath.Join(harness.OSHome, ".codex"),
		filepath.Join(harness.OSHome, ".claude"),
		filepath.Join(harness.OSHome, ".openclaw"),
		filepath.Join(harness.OSHome, ".gnupg"),
		filepath.Join(harness.OSHome, ".config", "git"),
		filepath.Join(harness.OSHome, "AppData", "Roaming"),
		filepath.Join(harness.OSHome, "AppData", "Local"),
		harness.PicoHome,
		harness.Workspace,
		filepath.Dir(harness.EventDB),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create gateway test runtime directory %q: %v", dir, err)
		}
	}
	if err := os.WriteFile(harness.EventDB, nil, 0o600); err != nil {
		t.Fatalf("create gateway test event database: %v", err)
	}

	t.Setenv("HOME", harness.OSHome)
	t.Setenv("USERPROFILE", harness.OSHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(harness.OSHome, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(harness.OSHome, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(harness.OSHome, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(harness.OSHome, ".local", "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(harness.OSHome, ".run"))
	t.Setenv("CODEX_HOME", filepath.Join(harness.OSHome, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(harness.OSHome, ".claude"))
	t.Setenv("OPENCLAW_HOME", filepath.Join(harness.OSHome, ".openclaw"))
	t.Setenv("GNUPGHOME", filepath.Join(harness.OSHome, ".gnupg"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(harness.OSHome, ".config", "git", "config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("APPDATA", filepath.Join(harness.OSHome, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(harness.OSHome, "AppData", "Local"))
	t.Setenv(config.EnvHome, harness.PicoHome)
	t.Setenv(config.EnvConfig, harness.Config)
	t.Setenv(config.EnvBinary, harness.Binary)

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = harness.Workspace
	cfg.Events.Ingress.DatabasePath = harness.EventDB
	cfg.Gateway.Host = "127.0.0.1"
	port, err := allocateAPITestLoopbackPort()
	if err != nil {
		t.Fatalf("allocate gateway test port: %v", err)
	}
	cfg.Gateway.Port = port
	if configure != nil {
		configure(cfg)
	}
	if err := config.SaveConfig(harness.Config, cfg); err != nil {
		t.Fatalf("save gateway test config: %v", err)
	}

	harness.Handler = NewHandler(harness.Config)
	return harness
}

func (h *gatewayRuntimeHarness) assertOwnedPath(t *testing.T, path string) {
	t.Helper()
	inside, err := pathWithinAPITestRoot(h.Root, path)
	if err != nil {
		t.Fatalf("resolve gateway runtime path %q: %v", path, err)
	}
	if !inside {
		t.Fatalf("gateway runtime path %q escapes test root %q", path, h.Root)
	}
}
