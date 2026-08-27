package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

func TestLoadEnvFile(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		expected  map[string]string
		expectErr bool
	}{
		{
			name: "basic env file",
			content: `API_KEY=secret123
DATABASE_URL=postgres://localhost/db
PORT=8080`,
			expected: map[string]string{
				"API_KEY":      "secret123",
				"DATABASE_URL": "postgres://localhost/db",
				"PORT":         "8080",
			},
			expectErr: false,
		},
		{
			name: "with comments and empty lines",
			content: `# This is a comment
API_KEY=secret123

# Another comment
DATABASE_URL=postgres://localhost/db

PORT=8080`,
			expected: map[string]string{
				"API_KEY":      "secret123",
				"DATABASE_URL": "postgres://localhost/db",
				"PORT":         "8080",
			},
			expectErr: false,
		},
		{
			name: "with quoted values",
			content: `API_KEY="secret with spaces"
NAME='single quoted'
PLAIN=no-quotes`,
			expected: map[string]string{
				"API_KEY": "secret with spaces",
				"NAME":    "single quoted",
				"PLAIN":   "no-quotes",
			},
			expectErr: false,
		},
		{
			name: "with spaces around equals",
			content: `API_KEY = secret123
DATABASE_URL= postgres://localhost/db
PORT =8080`,
			expected: map[string]string{
				"API_KEY":      "secret123",
				"DATABASE_URL": "postgres://localhost/db",
				"PORT":         "8080",
			},
			expectErr: false,
		},
		{
			name:      "invalid format - no equals",
			content:   `INVALID_LINE`,
			expectErr: true,
		},
		{
			name:      "empty file",
			content:   ``,
			expected:  map[string]string{},
			expectErr: false,
		},
		{
			name: "only comments",
			content: `# Comment 1
# Comment 2`,
			expected:  map[string]string{},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			envFile := filepath.Join(tmpDir, ".env")

			if err := os.WriteFile(envFile, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			result, err := loadEnvFile(envFile)

			if tt.expectErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d variables, got %d", len(tt.expected), len(result))
			}

			for key, expectedValue := range tt.expected {
				if actualValue, ok := result[key]; !ok {
					t.Errorf("Expected key %s not found", key)
				} else if actualValue != expectedValue {
					t.Errorf("For key %s: expected %q, got %q", key, expectedValue, actualValue)
				}
			}
		})
	}
}

func TestLoadEnvFileNotFound(t *testing.T) {
	_, err := loadEnvFile("/nonexistent/file.env")
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestExpandPolicyHomeCommandPath(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	policy := isolation.NewExecutionPolicy(config.IsolationConfig{
		EnvironmentAllowlist: []string{"HOME", "USERPROFILE"},
	})

	want := filepath.Join(homeDir, "bin", "my-mcp")
	got, err := expandPolicyHomeCommandPath(
		"~"+string(os.PathSeparator)+filepath.Join("bin", "my-mcp"),
		policy,
	)
	if err != nil || got != want {
		t.Fatalf("expandPolicyHomeCommandPath() = %q, %v; want %q", got, err, want)
	}

	if got, err = expandPolicyHomeCommandPath("npx", policy); err != nil || got != "npx" {
		t.Fatalf("bare command = %q, %v", got, err)
	}
}

func TestEnvFilePriority(t *testing.T) {
	// Create a temporary .env file
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	envContent := `API_KEY=from_file
DATABASE_URL=from_file
SHARED_VAR=from_file`

	if err := os.WriteFile(envFile, []byte(envContent), 0o644); err != nil {
		t.Fatalf("Failed to create .env file: %v", err)
	}

	// Load envFile
	envVars, err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("Failed to load env file: %v", err)
	}

	// Verify envFile variables
	if envVars["API_KEY"] != "from_file" {
		t.Errorf("Expected API_KEY=from_file, got %s", envVars["API_KEY"])
	}

	// Simulate config.Env overriding envFile
	configEnv := map[string]string{
		"SHARED_VAR": "from_config",
		"NEW_VAR":    "from_config",
	}

	// Merge: envFile first, then config overrides
	merged := make(map[string]string)
	for k, v := range envVars {
		merged[k] = v
	}
	for k, v := range configEnv {
		merged[k] = v
	}

	// Verify priority: config.Env should override envFile
	if merged["SHARED_VAR"] != "from_config" {
		t.Errorf(
			"Expected SHARED_VAR=from_config (config should override file), got %s",
			merged["SHARED_VAR"],
		)
	}
	if merged["API_KEY"] != "from_file" {
		t.Errorf("Expected API_KEY=from_file, got %s", merged["API_KEY"])
	}
	if merged["NEW_VAR"] != "from_config" {
		t.Errorf("Expected NEW_VAR=from_config, got %s", merged["NEW_VAR"])
	}
}

func TestBuildMCPStdioEnvironmentExplicitOnlySortedAndDetached(t *testing.T) {
	t.Setenv("MCP_AMBIENT_CANARY", "must-not-be-imported")

	envFile := filepath.Join(t.TempDir(), ".env")
	envContent := `ZED=from_file
SHARED=first_file_value
FILE_EMPTY=
QUOTED="file value"
SHARED=last_file_value`
	if err := os.WriteFile(envFile, []byte(envContent), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg := config.MCPServerConfig{
		EnvFile: envFile,
		Env: map[string]string{
			"ALPHA":        "from_config",
			"CONFIG_EMPTY": "",
			"SHARED":       "from_config",
		},
	}
	want := []string{
		"ALPHA=from_config",
		"CONFIG_EMPTY=",
		"FILE_EMPTY=",
		"QUOTED=file value",
		"SHARED=from_config",
		"ZED=from_file",
	}

	for attempt := 0; attempt < 20; attempt++ {
		got, err := buildMCPStdioEnvironmentForPlatform(cfg, "linux")
		if err != nil {
			t.Fatalf("buildMCPStdioEnvironmentForPlatform() error = %v", err)
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("environment = %#v, want %#v", got, want)
		}
		for _, entry := range got {
			if strings.HasPrefix(entry, "MCP_AMBIENT_CANARY=") {
				t.Fatalf("ambient environment leaked into explicit projection: %#v", got)
			}
		}
		got[0] = "MUTATED=caller"
	}
}

func TestBuildMCPStdioEnvironmentWindowsFoldingAndAmbiguity(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("Path=from_file\nPATH=last_file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := buildMCPStdioEnvironmentForPlatform(config.MCPServerConfig{
		EnvFile: envFile,
		Env:     map[string]string{"Path": "from_config", "empty": ""},
	}, "windows")
	if err != nil {
		t.Fatalf("buildMCPStdioEnvironmentForPlatform() error = %v", err)
	}
	want := []string{"EMPTY=", "PATH=from_config"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Windows environment = %#v, want %#v", got, want)
	}

	_, err = buildMCPStdioEnvironmentForPlatform(config.MCPServerConfig{
		Env: map[string]string{"Path": "one", "PATH": "two"},
	}, "windows")
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("case-alias error = %v, want deterministic conflict", err)
	}
}

func TestBuildMCPStdioEnvironmentValidatesBoundsWithoutValuesInErrors(t *testing.T) {
	secretValue := strings.Repeat("secret-canary", maxMCPEnvironmentValueBytes)
	_, err := buildMCPStdioEnvironmentForPlatform(config.MCPServerConfig{
		Env: map[string]string{"TOO_LARGE": secretValue},
	}, "linux")
	if err == nil {
		t.Fatal("expected oversized value to fail")
	}
	if strings.Contains(err.Error(), secretValue) || !strings.Contains(err.Error(), "TOO_LARGE") {
		t.Fatalf("oversized value error discloses value or omits key: %q", err)
	}

	_, err = buildMCPStdioEnvironmentForPlatform(config.MCPServerConfig{
		Env: map[string]string{"BAD=NAME": "value"},
	}, "linux")
	if err == nil || !strings.Contains(err.Error(), "BAD=NAME") {
		t.Fatalf("invalid-name error = %v", err)
	}

	tooMany := make(map[string]string, maxMCPEnvironmentEntries+1)
	for i := 0; i <= maxMCPEnvironmentEntries; i++ {
		tooMany[fmt.Sprintf("ENTRY_%03d", i)] = "value"
	}
	_, err = buildMCPStdioEnvironmentForPlatform(config.MCPServerConfig{Env: tooMany}, "linux")
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("entry-count error = %v", err)
	}
}

const mcpEnvironmentHelperMarker = "PICOCLAW_MCP_ENVIRONMENT_HELPER"

func TestMCPStdioEnvironmentHelper(t *testing.T) {
	if os.Getenv(mcpEnvironmentHelperMarker) != "1" {
		return
	}

	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "environment-helper",
		Version: "1.0.0",
	}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "environment"}, func(
		_ context.Context,
		_ *sdkmcp.CallToolRequest,
		_ map[string]any,
	) (*sdkmcp.CallToolResult, any, error) {
		values := map[string]string{}
		for _, name := range []string{
			"HOME",
			"MCP_AMBIENT_CANARY",
			"MCP_CONFIG_EMPTY",
			"MCP_FILE_EMPTY",
			"MCP_FILE_ONLY",
			"MCP_SHARED",
			"MCP_POLICY_GENERATION",
			"PATH",
		} {
			value, ok := os.LookupEnv(name)
			if !ok {
				value = "<absent>"
			}
			values[name] = value
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, nil, err
		}
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: string(encoded)},
		}}, nil, nil
	})

	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestMCPStdioEnvironmentPolicyIntegration(t *testing.T) {
	t.Setenv("MCP_AMBIENT_CANARY", "must-not-reach-helper")

	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte(strings.Join([]string{
		mcpEnvironmentHelperMarker + "=1",
		"MCP_FILE_ONLY=from_file",
		"MCP_FILE_EMPTY=",
		"MCP_SHARED=from_file",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	policy := mcpStdioTestExecutionPolicy(config.DefaultConfig().Isolation)
	mgr := NewManagerWithExecutionPolicy(policy)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() {
		if err := mgr.Close(); err != nil {
			t.Errorf("Manager.Close() error = %v", err)
		}
		cancel()
	})
	if err := mgr.ConnectServer(ctx, "environment", config.MCPServerConfig{
		Enabled: true,
		Type:    "stdio",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPStdioEnvironmentHelper$"},
		EnvFile: envFile,
		Env: map[string]string{
			"MCP_CONFIG_EMPTY": "",
			"MCP_SHARED":       "from_config",
		},
	}); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}

	result, err := mgr.CallTool(ctx, "environment", "environment", nil)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("CallTool() result = %#v", result)
	}
	content, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("CallTool() content type = %T, want *mcp.TextContent", result.Content[0])
	}
	got := map[string]string{}
	if err := json.Unmarshal([]byte(content.Text), &got); err != nil {
		t.Fatalf("Unmarshal(tool result) error = %v", err)
	}
	want := map[string]string{
		"HOME":                  environmentPolicyValueOrAbsent(policy, "HOME"),
		"MCP_AMBIENT_CANARY":    "<absent>",
		"MCP_CONFIG_EMPTY":      "",
		"MCP_FILE_EMPTY":        "",
		"MCP_FILE_ONLY":         "from_file",
		"MCP_SHARED":            "from_config",
		"MCP_POLICY_GENERATION": "<absent>",
		"PATH":                  environmentPolicyValueOrAbsent(policy, "PATH"),
	}
	if len(got) != len(want) {
		t.Fatalf("helper environment = %#v, want %#v", got, want)
	}
	for name, wantValue := range want {
		if gotValue, ok := got[name]; !ok || gotValue != wantValue {
			t.Fatalf("helper environment = %#v, want %#v", got, want)
		}
	}
}

func TestMCPStdioProcessRetainsPolicyAfterConnectContextAndReconnect(t *testing.T) {
	homeA := t.TempDir()
	homeB := t.TempDir()
	helperName := "p014-mcp-helper" + filepath.Ext(os.Args[0])
	helperPath := filepath.Join(homeA, helperName)
	testBinary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(helperPath, testBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeA)
	t.Setenv("USERPROFILE", homeA)
	t.Setenv("MCP_POLICY_GENERATION", "generation-a")
	policy := mcpStdioTestExecutionPolicy(config.IsolationConfig{
		EnvironmentAllowlist: []string{
			"PATH",
			"PATHEXT",
			"HOME",
			"USERPROFILE",
			"MCP_POLICY_GENERATION",
		},
	})
	mgr := NewManagerWithExecutionPolicy(policy)
	t.Cleanup(func() { _ = mgr.Close() })
	cfg := config.MCPServerConfig{
		Enabled: true,
		Type:    "stdio",
		Command: "~/" + helperName,
		Args:    []string{"-test.run=^TestMCPStdioEnvironmentHelper$"},
		Env: map[string]string{
			mcpEnvironmentHelperMarker: "1",
		},
	}
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
	if err = mgr.ConnectServer(connectCtx, "frozen", cfg); err != nil {
		cancelConnect()
		t.Fatal(err)
	}
	initial, ok := mgr.GetServer("frozen")
	if !ok {
		t.Fatal("stdio server missing after connect")
	}
	cancelConnect()

	callAndCheckMCPPolicyEnvironment(t, mgr, "frozen", homeA, "generation-a")
	if afterCall, present := mgr.GetServer("frozen"); !present || afterCall != initial {
		t.Fatal("canceling connect context replaced the established stdio connection")
	}

	t.Setenv("HOME", homeB)
	t.Setenv("USERPROFILE", homeB)
	t.Setenv("MCP_POLICY_GENERATION", "live-generation-b")
	stale, ok := mgr.GetServer("frozen")
	if !ok {
		t.Fatal("stdio server missing before reconnect")
	}
	reconnectCtx, cancelReconnect := context.WithTimeout(context.Background(), 10*time.Second)
	fresh, err := mgr.reconnectServer(reconnectCtx, "frozen", stale)
	cancelReconnect()
	if err != nil || fresh == stale {
		t.Fatalf("real stdio reconnect = %#v, %v", fresh, err)
	}
	callAndCheckMCPPolicyEnvironment(t, mgr, "frozen", homeA, "generation-a")
	if afterReconnectCall, present := mgr.GetServer("frozen"); !present || afterReconnectCall != fresh {
		t.Fatal("canceling reconnect context replaced the fresh stdio connection")
	}
}

func TestMCPStdioDuplicateConnectClosesReplacedProcess(t *testing.T) {
	policy := mcpStdioTestExecutionPolicy(config.DefaultConfig().Isolation)
	mgr := NewManagerWithExecutionPolicy(policy)
	t.Cleanup(func() { _ = mgr.Close() })
	cfg := config.MCPServerConfig{
		Enabled: true,
		Type:    "stdio",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPStdioEnvironmentHelper$"},
		Env: map[string]string{
			mcpEnvironmentHelperMarker: "1",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.ConnectServer(ctx, "replace", cfg); err != nil {
		t.Fatal(err)
	}
	old, _ := mgr.GetServer("replace")
	if err := mgr.ConnectServer(ctx, "replace", cfg); err != nil {
		t.Fatal(err)
	}
	fresh, _ := mgr.GetServer("replace")
	if old == nil || fresh == nil || old == fresh {
		t.Fatalf("replacement connections = old:%p fresh:%p", old, fresh)
	}
	oldCtx, cancelOld := context.WithTimeout(context.Background(), time.Second)
	defer cancelOld()
	if _, err := old.Session.CallTool(oldCtx, &sdkmcp.CallToolParams{Name: "environment"}); err == nil {
		t.Fatal("replaced stdio session remained callable")
	}
	callAndCheckMCPPolicyEnvironment(
		t,
		mgr,
		"replace",
		environmentPolicyValueOrAbsent(policy, "HOME"),
		"<absent>",
	)
}

func callAndCheckMCPPolicyEnvironment(
	t *testing.T,
	mgr *Manager,
	server,
	wantHome,
	wantGeneration string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := mgr.CallTool(ctx, server, "environment", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("tool result = %#v", result)
	}
	content, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("tool content type = %T", result.Content[0])
	}
	values := map[string]string{}
	if err = json.Unmarshal([]byte(content.Text), &values); err != nil {
		t.Fatal(err)
	}
	if values["HOME"] != wantHome ||
		values["MCP_POLICY_GENERATION"] != wantGeneration {
		t.Fatalf("stdio policy environment = %#v", values)
	}
}

func environmentPolicyValueOrAbsent(
	policy isolation.ExecutionPolicy,
	name string,
) string {
	value, ok := policy.LookupEnvironment(name)
	if !ok {
		return "<absent>"
	}
	return value
}

func TestLoadFromMCPConfig_EmptyWorkspaceWithRelativeEnvFile(t *testing.T) {
	mgr := NewManager()

	mcpCfg := config.MCPConfig{
		ToolConfig: config.ToolConfig{
			Enabled: true,
		},
		Servers: map[string]config.MCPServerConfig{
			"test-server": {
				Enabled: true,
				Command: "echo",
				Args:    []string{"ok"},
				EnvFile: ".env",
			},
		},
	}

	err := mgr.LoadFromMCPConfig(context.Background(), mcpCfg, "")
	if err == nil {
		t.Fatal("expected error for relative env_file with empty workspace path, got nil")
	}

	if !strings.Contains(err.Error(), "workspace path is empty") {
		t.Fatalf("expected workspace path validation error, got: %v", err)
	}
}

func TestNewManager_InitialState(t *testing.T) {
	mgr := NewManager()
	if mgr == nil {
		t.Fatal("expected manager instance, got nil")
	}
	if len(mgr.GetServers()) != 0 {
		t.Fatalf("expected no servers on new manager, got %d", len(mgr.GetServers()))
	}
}

func TestManagerCarriesExactExecutionPolicyOnInitialConnect(t *testing.T) {
	exactPolicy := isolation.NewExecutionPolicy(config.IsolationConfig{
		ExposePaths: []config.ExposePath{{Source: "/policy-a", Mode: "ro"}},
	})
	mgr := NewManagerWithExecutionPolicy(exactPolicy)
	var received isolation.ExecutionPolicy
	mgr.connect = func(
		_ context.Context,
		name string,
		cfg config.MCPServerConfig,
		policy isolation.ExecutionPolicy,
	) (*ServerConnection, error) {
		received = policy
		return &ServerConnection{Name: name, Config: cfg}, nil
	}

	if err := mgr.ConnectServer(context.Background(), "exact", config.MCPServerConfig{
		Type:    "stdio",
		Command: "unused",
	}); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}
	if received != exactPolicy {
		t.Fatal("initial connection did not receive the manager's exact execution policy")
	}

	defaultManager := NewManager()
	if defaultManager.executionPolicy == (isolation.ExecutionPolicy{}) {
		t.Fatal("default manager must retain a valid compatibility execution policy")
	}

	strictZero := NewManagerWithExecutionPolicy(isolation.ExecutionPolicy{})
	if strictZero.executionPolicy != (isolation.ExecutionPolicy{}) {
		t.Fatal("explicit zero execution policy must not fall back to compatibility policy")
	}
}

func TestIsolatedCommandTransportStrictZeroPolicyFailsBeforeStart(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestIsolatedCommandTransportStrictZeroPolicyFailsBeforeStart$")
	transport := &isolatedCommandTransport{
		Command:         cmd,
		ExecutionPolicy: isolation.ExecutionPolicy{},
	}

	connection, err := transport.Connect(context.Background())
	if connection != nil {
		_ = connection.Close()
		t.Fatal("Connect() returned a connection for an unavailable execution policy")
	}
	if !errors.Is(err, isolation.ErrExecutionPolicyUnavailable) {
		t.Fatalf("Connect() error = %v, want %v", err, isolation.ErrExecutionPolicyUnavailable)
	}
	if cmd.Process != nil {
		t.Fatalf("strict zero policy started process PID %d", cmd.Process.Pid)
	}
}

func mcpStdioTestExecutionPolicy(
	isolationCfg config.IsolationConfig,
) isolation.ExecutionPolicy {
	if isolationCfg.EnvironmentAllowlist == nil {
		isolationCfg.EnvironmentAllowlist = config.DefaultIsolationEnvironmentAllowlist()
	} else {
		isolationCfg.EnvironmentAllowlist = append(
			[]string(nil),
			isolationCfg.EnvironmentAllowlist...,
		)
	}
	for _, name := range isolationCfg.EnvironmentAllowlist {
		if strings.EqualFold(name, "GOCOVERDIR") {
			return isolation.NewExecutionPolicy(isolationCfg)
		}
	}
	isolationCfg.EnvironmentAllowlist = append(isolationCfg.EnvironmentAllowlist, "GOCOVERDIR")
	return isolation.NewExecutionPolicy(isolationCfg)
}

func TestConnectServerPublishesRuntimeEvents(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(
		runtimeevents.KindMCPServerConnected,
		runtimeevents.KindMCPServerFailed,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "mcp-events", Buffer: 2})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	mgr := NewManager(WithRuntimeEvents(eventBus))
	mgr.connect = func(
		_ context.Context,
		name string,
		cfg config.MCPServerConfig,
		_ isolation.ExecutionPolicy,
	) (*ServerConnection, error) {
		if name == "bad" {
			return nil, fmt.Errorf("connect failed")
		}
		return &ServerConnection{
			Name:   name,
			Config: cfg,
			Tools:  []*sdkmcp.Tool{{Name: "echo"}},
		}, nil
	}

	err = mgr.ConnectServer(context.Background(), "good", config.MCPServerConfig{
		Type:    "stdio",
		Command: "echo",
	})
	if err != nil {
		t.Fatalf("ConnectServer(good) error = %v", err)
	}
	connected := receiveMCPRuntimeEvent(t, eventsCh)
	if connected.Kind != runtimeevents.KindMCPServerConnected ||
		connected.Source.Name != "good" ||
		connected.Severity != runtimeevents.SeverityInfo {
		t.Fatalf("connected event = %+v", connected)
	}
	if connected.Attrs["server"] != "good" ||
		connected.Attrs["type"] != "stdio" ||
		connected.Attrs["tool_count"] != 1 {
		t.Fatalf("connected attrs = %#v", connected.Attrs)
	}

	err = mgr.ConnectServer(context.Background(), "bad", config.MCPServerConfig{
		Type:    "stdio",
		Command: "echo",
	})
	if err == nil {
		t.Fatal("expected ConnectServer(bad) to fail")
	}
	failed := receiveMCPRuntimeEvent(t, eventsCh)
	if failed.Kind != runtimeevents.KindMCPServerFailed ||
		failed.Source.Name != "bad" ||
		failed.Severity != runtimeevents.SeverityError {
		t.Fatalf("failed event = %+v", failed)
	}
	if failed.Attrs["server"] != "bad" || failed.Attrs["error"] != "connect failed" {
		t.Fatalf("failed attrs = %#v", failed.Attrs)
	}
}

func receiveMCPRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("runtime event channel closed before expected event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event")
		return runtimeevents.Event{}
	}
}

func TestLoadFromMCPConfig_DisabledOrEmptyServers(t *testing.T) {
	mgr := NewManager()

	err := mgr.LoadFromMCPConfig(
		context.Background(),
		config.MCPConfig{ToolConfig: config.ToolConfig{Enabled: false}},
		"/tmp",
	)
	if err != nil {
		t.Fatalf("expected nil error when MCP disabled, got: %v", err)
	}

	err = mgr.LoadFromMCPConfig(
		context.Background(),
		config.MCPConfig{ToolConfig: config.ToolConfig{Enabled: true}},
		"/tmp",
	)
	if err != nil {
		t.Fatalf("expected nil error when no servers configured, got: %v", err)
	}
}

func TestGetServers_ReturnsCopy(t *testing.T) {
	mgr := NewManager()
	mgr.servers["s1"] = &ServerConnection{Name: "s1"}

	servers := mgr.GetServers()
	delete(servers, "s1")

	if _, ok := mgr.GetServer("s1"); !ok {
		t.Fatal("expected internal manager state to remain unchanged")
	}
}

func TestGetAllTools_FiltersEmptyTools(t *testing.T) {
	mgr := NewManager()
	mgr.servers["empty"] = &ServerConnection{Name: "empty", Tools: nil}
	mgr.servers["with-tools"] = &ServerConnection{Name: "with-tools", Tools: []*sdkmcp.Tool{{}}}

	all := mgr.GetAllTools()
	if _, ok := all["empty"]; ok {
		t.Fatal("expected server without tools to be excluded")
	}
	if _, ok := all["with-tools"]; !ok {
		t.Fatal("expected server with tools to be included")
	}
}

func TestCallTool_ErrorsForClosedOrMissingServer(t *testing.T) {
	t.Run("manager closed", func(t *testing.T) {
		mgr := NewManager()
		mgr.closed.Store(true)

		_, err := mgr.CallTool(context.Background(), "s1", "tool", nil)
		if err == nil || !strings.Contains(err.Error(), "manager is closed") {
			t.Fatalf("expected manager closed error, got: %v", err)
		}
	})

	t.Run("server missing", func(t *testing.T) {
		mgr := NewManager()

		_, err := mgr.CallTool(context.Background(), "missing", "tool", nil)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected server not found error, got: %v", err)
		}
	})
}

func TestWorkflowAuthoringIdentitySafetyTracksManagerMutations(t *testing.T) {
	manager := NewManager()
	setTools := func(toolNames ...string) {
		t.Helper()
		listed := make([]*sdkmcp.Tool, 0, len(toolNames))
		for _, toolName := range toolNames {
			listed = append(listed, &sdkmcp.Tool{Name: toolName})
		}
		manager.mu.Lock()
		manager.servers = map[string]*ServerConnection{
			"github": {
				Name:  "github",
				Tools: listed,
			},
		}
		manager.refreshWorkflowAuthoringIdentitySafetyLocked()
		manager.mu.Unlock()
	}

	setTools("echo", "echo")
	if !manager.WorkflowAuthoringIdentitiesSafe() {
		t.Fatal("exact duplicate identity marked unsafe")
	}

	setTools("Search", "search")
	if manager.WorkflowAuthoringIdentitiesSafe() {
		t.Fatal("distinct canonical collision marked safe")
	}
	visited := false
	identitySafe, err := manager.VisitWorkflowAuthoringServers(
		context.Background(),
		func(string, *ServerConnection) bool {
			visited = true
			return true
		},
	)
	if err != nil || identitySafe || visited {
		t.Fatalf("collision visit = safe %v, visited %v, error %v", identitySafe, visited, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, canceledErr := manager.VisitWorkflowAuthoringServers(
		canceled,
		func(string, *ServerConnection) bool { return true },
	); canceledErr != context.Canceled {
		t.Fatalf("canceled collision visit error = %v, want context.Canceled", canceledErr)
	}

	setTools("echo", "list")
	if !manager.WorkflowAuthoringIdentitiesSafe() {
		t.Fatal("collision-safe refresh did not recover manager state")
	}
	identitySafe, err = manager.VisitWorkflowAuthoringServers(
		context.Background(),
		func(name string, connection *ServerConnection) bool {
			visited = name == "github" && connection != nil
			return true
		},
	)
	if err != nil || !identitySafe || !visited {
		t.Fatalf("refreshed visit = safe %v, visited %v, error %v", identitySafe, visited, err)
	}
}

func TestConnectServer_StreamableHTTPRequestResponseMode(t *testing.T) {
	t.Parallel()

	for _, transportType := range []string{"http", "streamable-http"} {
		t.Run(transportType, func(t *testing.T) {
			t.Parallel()

			server := sdkmcp.NewServer(&sdkmcp.Implementation{
				Name:    "streamable-test-server",
				Version: "1.0.0",
			}, nil)
			sdkmcp.AddTool(server, &sdkmcp.Tool{
				Name:        "echo",
				Description: "Echo test tool",
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args map[string]any) (*sdkmcp.CallToolResult, any, error) {
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{
						&sdkmcp.TextContent{Text: "ok"},
					},
				}, nil, nil
			})

			type observedRequest struct {
				Method        string
				SessionID     string
				Authorization string
			}

			var (
				mu       sync.Mutex
				observed []observedRequest
			)

			handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
				return server
			}, nil)
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				observed = append(observed, observedRequest{
					Method:        r.Method,
					SessionID:     r.Header.Get("Mcp-Session-Id"),
					Authorization: r.Header.Get("Authorization"),
				})
				mu.Unlock()
				handler.ServeHTTP(w, r)
			}))
			defer httpServer.Close()

			conn, err := connectServer(context.Background(), "streamable", config.MCPServerConfig{
				Enabled: true,
				Type:    transportType,
				URL:     httpServer.URL,
				Headers: map[string]string{
					"Authorization": "Bearer test-token",
				},
			})
			if err != nil {
				t.Fatalf("connectServer(%q) error = %v", transportType, err)
			}
			if got := len(conn.Tools); got != 1 {
				t.Fatalf("len(conn.Tools) = %d, want 1", got)
			}
			if got := conn.Session.ID(); got == "" {
				t.Fatal("expected non-empty streamable session ID")
			}
			if err := conn.Session.Close(); err != nil {
				t.Fatalf("Session.Close() error = %v", err)
			}

			mu.Lock()
			defer mu.Unlock()

			var (
				getCount            int
				postCount           int
				deleteCount         int
				postWithSession     bool
				deleteWithSession   bool
				requestsWithAuth    int
				requestsWithoutAuth []string
			)

			for _, req := range observed {
				switch req.Method {
				case http.MethodGet:
					getCount++
				case http.MethodPost:
					postCount++
					if req.SessionID != "" {
						postWithSession = true
					}
				case http.MethodDelete:
					deleteCount++
					if req.SessionID != "" {
						deleteWithSession = true
					}
				}

				if req.Authorization == "Bearer test-token" {
					requestsWithAuth++
				} else {
					requestsWithoutAuth = append(requestsWithoutAuth, req.Method)
				}
			}

			if getCount != 0 {
				t.Fatalf("expected no standalone GET requests for %q transport, saw %d", transportType, getCount)
			}
			if postCount == 0 {
				t.Fatal("expected POST requests during streamable HTTP handshake")
			}
			if deleteCount != 1 {
				t.Fatalf("DELETE count = %d, want 1", deleteCount)
			}
			if !postWithSession {
				t.Fatal("expected at least one POST request with Mcp-Session-Id")
			}
			if !deleteWithSession {
				t.Fatal("expected DELETE request with Mcp-Session-Id")
			}
			if requestsWithAuth != len(observed) {
				t.Fatalf("Authorization header missing on requests: %v", requestsWithoutAuth)
			}
		})
	}
}

func TestConnectServerRejectsUnsafeRemoteURLs(t *testing.T) {
	for _, test := range []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{name: "unsupported scheme", rawURL: "ftp://example.com/mcp", wantErr: "HTTP or HTTPS"},
		{name: "embedded credentials", rawURL: "https://user:secret@example.com/mcp", wantErr: "embedded credentials"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := connectServer(context.Background(), "unsafe", config.MCPServerConfig{
				Enabled: true,
				Type:    "http",
				URL:     test.rawURL,
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("connectServer() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}

	_, err := connectServer(context.Background(), "cleartext-headers", config.MCPServerConfig{
		Enabled: true,
		Type:    "http",
		URL:     "http://mcp.example.test/api",
		Headers: map[string]string{"X-API-Key": "secret"},
	})
	if err == nil || !strings.Contains(err.Error(), "custom headers require HTTPS") {
		t.Fatalf("connectServer() cleartext-header error = %v, want HTTPS requirement", err)
	}
}

func TestCallTool_ReconnectsWhenHTTPServerLosesSession(t *testing.T) {
	staleConn, staleTransport, err := newScriptedServerConnection(
		"session-1",
		nil,
		fmt.Errorf(`sending "tools/call": failed to connect (session ID: session-1): %w`, sdkmcp.ErrSessionMissing),
	)
	if err != nil {
		t.Fatalf("newScriptedServerConnection(stale) error = %v", err)
	}
	freshConn, freshTransport, err := newScriptedServerConnection(
		"session-2",
		&sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "reconnected"},
			},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("newScriptedServerConnection(fresh) error = %v", err)
	}

	exactPolicy := isolation.NewExecutionPolicy(config.IsolationConfig{
		ExposePaths: []config.ExposePath{{Source: "/reconnect-policy", Mode: "ro"}},
	})
	mgr := NewManagerWithExecutionPolicy(exactPolicy)
	connectCalls := 0
	var reconnectPolicy isolation.ExecutionPolicy
	mgr.connect = func(
		ctx context.Context,
		name string,
		cfg config.MCPServerConfig,
		policy isolation.ExecutionPolicy,
	) (*ServerConnection, error) {
		connectCalls++
		reconnectPolicy = policy
		if connectCalls == 1 {
			return freshConn, nil
		}
		return nil, fmt.Errorf("unexpected reconnect attempt %d", connectCalls)
	}

	mgr.servers["flaky"] = staleConn

	result, err := mgr.CallTool(context.Background(), "flaky", "echo", map[string]any{
		"query": "hello",
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("CallTool() returned unexpected content: %#v", result)
	}

	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("CallTool() content type = %T, want *sdkmcp.TextContent", result.Content[0])
	}
	if text.Text != "reconnected" {
		t.Fatalf("CallTool() text = %q, want %q", text.Text, "reconnected")
	}

	conn, ok := mgr.GetServer("flaky")
	if !ok {
		t.Fatal("expected flaky server to remain connected after reconnect")
	}
	if conn.Session.ID() != "session-2" {
		t.Fatalf("Session.ID() = %q, want %q", conn.Session.ID(), "session-2")
	}
	if connectCalls != 1 {
		t.Fatalf("connectCalls = %d, want 1", connectCalls)
	}
	if reconnectPolicy != exactPolicy {
		t.Fatal("reconnect did not receive the manager's exact retained execution policy")
	}
	if staleTransport.toolCallCalls != 1 {
		t.Fatalf("stale toolCallCalls = %d, want 1", staleTransport.toolCallCalls)
	}
	if freshTransport.toolCallCalls != 1 {
		t.Fatalf("fresh toolCallCalls = %d, want 1", freshTransport.toolCallCalls)
	}
}

func TestConnectAndReconnectCancellationCannotPublish(t *testing.T) {
	connection, transport, err := newScriptedServerConnection("canceled", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	mgr.connect = func(
		context.Context,
		string,
		config.MCPServerConfig,
		isolation.ExecutionPolicy,
	) (*ServerConnection, error) {
		cancel()
		return connection, nil
	}
	if err = mgr.ConnectServer(ctx, "canceled", connection.Config); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ConnectServer error = %v", err)
	}
	if _, ok := mgr.GetServer("canceled"); ok {
		t.Fatal("canceled initial connection was published")
	}
	transport.mu.Lock()
	closed := transport.closed
	transport.mu.Unlock()
	if !closed {
		t.Fatal("canceled initial connection was not closed")
	}

	stale, _, err := newScriptedServerConnection("stale", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh, freshTransport, err := newScriptedServerConnection("fresh", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr = NewManager()
	mgr.servers["reconnect"] = stale
	ctx, cancel = context.WithCancel(context.Background())
	mgr.connect = func(
		context.Context,
		string,
		config.MCPServerConfig,
		isolation.ExecutionPolicy,
	) (*ServerConnection, error) {
		cancel()
		return fresh, nil
	}
	if _, err = mgr.reconnectServer(ctx, "reconnect", stale); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reconnect error = %v", err)
	}
	if got, _ := mgr.GetServer("reconnect"); got != stale {
		t.Fatal("canceled reconnect replaced stale connection")
	}
	freshTransport.mu.Lock()
	closed = freshTransport.closed
	freshTransport.mu.Unlock()
	if !closed {
		t.Fatal("canceled fresh reconnect was not closed")
	}
	_ = stale.Session.Close()
}

func TestManagerCloseRejectsAndJoinsConnectionAdmission(t *testing.T) {
	mgr := NewManager()
	entered := make(chan struct{})
	release := make(chan struct{})
	connection, transport, err := newScriptedServerConnection("closing", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.connect = func(
		context.Context,
		string,
		config.MCPServerConfig,
		isolation.ExecutionPolicy,
	) (*ServerConnection, error) {
		close(entered)
		<-release
		return connection, nil
	}
	connectDone := make(chan error, 1)
	go func() {
		connectDone <- mgr.ConnectServer(
			context.Background(),
			"closing",
			connection.Config,
		)
	}()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- mgr.Close() }()
	select {
	case err = <-closeDone:
		close(release)
		t.Fatalf("Close overtook connection admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err = <-connectDone; err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("in-flight connect after Close error = %v", err)
	}
	if err = <-closeDone; err != nil {
		t.Fatalf("Close error = %v", err)
	}
	transport.mu.Lock()
	closed := transport.closed
	transport.mu.Unlock()
	if !closed {
		t.Fatal("private connection was not closed during Close")
	}
	connectCalled := false
	mgr.connect = func(
		context.Context,
		string,
		config.MCPServerConfig,
		isolation.ExecutionPolicy,
	) (*ServerConnection, error) {
		connectCalled = true
		return nil, nil
	}
	err = mgr.ConnectServer(
		context.Background(),
		"after-close",
		config.MCPServerConfig{},
	)
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("ConnectServer after Close error = %v", err)
	}
	if connectCalled {
		t.Fatal("ConnectServer after Close invoked connector")
	}
}

func TestManagerCloseJoinsAdmittedToolCall(t *testing.T) {
	connection, transport, err := newScriptedServerConnection(
		"call-close",
		&sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "done"},
		}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	transport.toolCallEntered = make(chan struct{})
	transport.toolCallRelease = make(chan struct{})
	mgr := NewManager()
	mgr.servers["blocking"] = connection
	callDone := make(chan error, 1)
	go func() {
		_, callErr := mgr.CallTool(context.Background(), "blocking", "echo", nil)
		callDone <- callErr
	}()
	<-transport.toolCallEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- mgr.Close() }()
	select {
	case err = <-closeDone:
		close(transport.toolCallRelease)
		t.Fatalf("Close overtook admitted tool call: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(transport.toolCallRelease)
	if err = <-callDone; err != nil {
		t.Fatalf("CallTool error = %v", err)
	}
	if err = <-closeDone; err != nil {
		t.Fatalf("Close error = %v", err)
	}
}

func TestClose_IdempotentOnEmptyManager(t *testing.T) {
	mgr := NewManager()
	if !mgr.WorkflowAuthoringIdentitiesSafe() {
		t.Fatal("new manager identity state should be safe")
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("first close should succeed, got: %v", err)
	}
	if mgr.WorkflowAuthoringIdentitiesSafe() {
		t.Fatal("closed manager retained safe workflow-authoring identity state")
	}
	visited := false
	identitySafe, err := mgr.VisitWorkflowAuthoringServers(
		context.Background(),
		func(string, *ServerConnection) bool {
			visited = true
			return true
		},
	)
	if err != nil || identitySafe || visited {
		t.Fatalf("closed visit = safe %v, visited %v, error %v", identitySafe, visited, err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("second close should be idempotent, got: %v", err)
	}
}

func newScriptedServerConnection(
	sessionID string,
	toolCallResult *sdkmcp.CallToolResult,
	toolCallErr error,
) (*ServerConnection, *scriptedTransport, error) {
	transport := &scriptedTransport{
		sessionID:      sessionID,
		toolCallResult: toolCallResult,
		toolCallErr:    toolCallErr,
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "picoclaw-test",
		Version: "1.0.0",
	}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, nil, err
	}

	return &ServerConnection{
		Name:    "flaky",
		Config:  config.MCPServerConfig{Enabled: true, Type: "http", URL: "https://example.invalid/mcp"},
		Client:  client,
		Session: session,
		Tools: []*sdkmcp.Tool{
			{
				Name:        "echo",
				Description: "Echo test tool",
				InputSchema: map[string]any{"type": "object"},
			},
		},
	}, transport, nil
}

type scriptedTransport struct {
	sessionID       string
	toolCallResult  *sdkmcp.CallToolResult
	toolCallErr     error
	toolCallEntered chan struct{}
	toolCallRelease chan struct{}
	toolCallOnce    sync.Once

	mu            sync.Mutex
	toolCallCalls int
	closed        bool
	incoming      chan jsonrpc.Message
}

func (t *scriptedTransport) Connect(context.Context) (sdkmcp.Connection, error) {
	if t.incoming == nil {
		t.incoming = make(chan jsonrpc.Message, 4)
	}
	return t, nil
}

func (t *scriptedTransport) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-t.incoming:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	}
}

func (t *scriptedTransport) Write(ctx context.Context, msg jsonrpc.Message) error {
	req, ok := msg.(*jsonrpc.Request)
	if !ok {
		return nil
	}

	switch req.Method {
	case "initialize":
		payload, err := json.Marshal(&sdkmcp.InitializeResult{
			ProtocolVersion: "2025-11-25",
			ServerInfo: &sdkmcp.Implementation{
				Name:    "scripted-test-server",
				Version: "1.0.0",
			},
			Capabilities: &sdkmcp.ServerCapabilities{
				Tools: &sdkmcp.ToolCapabilities{},
			},
		})
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t.incoming <- &jsonrpc.Response{ID: req.ID, Result: payload}:
			return nil
		}

	case "notifications/initialized":
		return nil

	case "tools/call":
		t.mu.Lock()
		t.toolCallCalls++
		t.mu.Unlock()
		if t.toolCallEntered != nil {
			t.toolCallOnce.Do(func() { close(t.toolCallEntered) })
		}
		if t.toolCallRelease != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.toolCallRelease:
			}
		}

		if t.toolCallErr != nil {
			return t.toolCallErr
		}

		payload, err := json.Marshal(t.toolCallResult)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t.incoming <- &jsonrpc.Response{ID: req.ID, Result: payload}:
			return nil
		}
	}

	return fmt.Errorf("unexpected method %q", req.Method)
}

func (t *scriptedTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.incoming)
	return nil
}

func (t *scriptedTransport) SessionID() string {
	return t.sessionID
}
