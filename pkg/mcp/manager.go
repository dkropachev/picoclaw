package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// headerTransport is an http.RoundTripper that adds custom headers to requests
type headerTransport struct {
	base         http.RoundTripper
	headers      map[string]string
	tokenSource  oauth2.TokenSource
	originScheme string
	originHost   string
}

func expandPolicyHomeCommandPath(
	command string,
	policy isolation.ExecutionPolicy,
) (string, error) {
	if command == "" || command[0] != '~' {
		return command, nil
	}
	if command != "~" && !strings.HasPrefix(command, "~/") &&
		!strings.HasPrefix(command, "~\\") {
		return command, nil
	}
	home, ok := policy.LookupEnvironment("HOME")
	if (!ok || strings.TrimSpace(home) == "") && runtime.GOOS == "windows" {
		home, ok = policy.LookupEnvironment("USERPROFILE")
	}
	if (!ok || strings.TrimSpace(home) == "") && runtime.GOOS == "windows" {
		drive, driveOK := policy.LookupEnvironment("HOMEDRIVE")
		homePath, pathOK := policy.LookupEnvironment("HOMEPATH")
		if driveOK && pathOK {
			home = drive + homePath
			ok = true
		}
	}
	if !ok || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("MCP command home is unavailable in execution policy")
	}
	if strings.IndexByte(home, 0) >= 0 || !filepath.IsAbs(home) {
		return "", fmt.Errorf("MCP command home is invalid in execution policy")
	}
	if command == "~" {
		return home, nil
	}
	return filepath.Join(home, command[2:]), nil
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Bind credentials and custom headers to the configured MCP origin. The
	// transport is invoked again after redirects, so unconditional injection
	// here would defeat net/http's cross-origin credential stripping.
	if strings.EqualFold(req.URL.Scheme, t.originScheme) &&
		strings.EqualFold(req.URL.Host, t.originHost) {
		for key, value := range t.headers {
			if isReservedMCPTransportHeader(key) {
				continue
			}
			req.Header.Set(key, value)
		}
		if t.tokenSource != nil {
			token, err := t.tokenSource.Token()
			if err != nil {
				return nil, fmt.Errorf("resolve MCP stored access token: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		}
	} else {
		for key := range t.headers {
			req.Header.Del(key)
		}
		if t.tokenSource != nil {
			req.Header.Del("Authorization")
		}
	}

	// Use the base transport
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func isReservedMCPTransportHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "accept",
		"content-length",
		"content-type",
		"host",
		"mcp-protocol-version",
		"mcp-session-id":
		return true
	default:
		return false
	}
}

const (
	maxMCPEnvironmentEntries      = 256
	maxMCPEnvironmentNameBytes    = 128
	maxMCPEnvironmentValueBytes   = 16 * 1024
	maxMCPEncodedEnvironmentBytes = 24 * 1024
)

type mcpEnvironmentEntry struct {
	name  string
	value string
}

// loadEnvFile loads environment variables from a file in .env format
// Each line should be in the format: KEY=value
// Lines starting with # are comments
// Empty lines are ignored
func loadEnvFile(path string) (map[string]string, error) {
	entries, err := loadEnvFileEntries(path)
	if err != nil {
		return nil, err
	}

	envVars := make(map[string]string, len(entries))
	for _, entry := range entries {
		envVars[entry.name] = entry.value
	}
	return envVars, nil
}

func loadEnvFileEntries(path string) ([]mcpEnvironmentEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open env file: %w", err)
	}
	defer file.Close()

	var entries []mcpEnvironmentEntry
	scanner := bufio.NewScanner(file)
	lineNum := 0
	encodedBytes := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid format at line %d", lineNum)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return nil, fmt.Errorf("invalid format at line %d: empty key", lineNum)
		}

		// Remove surrounding quotes if present
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		if len(entries) == maxMCPEnvironmentEntries {
			return nil, fmt.Errorf(
				"env file has too many entries at line %d: maximum is %d",
				lineNum,
				maxMCPEnvironmentEntries,
			)
		}
		entry := mcpEnvironmentEntry{name: key, value: value}
		if err := validateMCPEnvironmentEntry("env_file", entry); err != nil {
			return nil, fmt.Errorf("invalid environment at line %d: %w", lineNum, err)
		}
		if len(entries) >= maxMCPEnvironmentEntries {
			return nil, fmt.Errorf(
				"env file has more than %d environment entries",
				maxMCPEnvironmentEntries,
			)
		}
		encodedBytes += len(key) + 1 + len(value) + 1
		if encodedBytes > maxMCPEncodedEnvironmentBytes {
			return nil, fmt.Errorf(
				"env file exceeds the %d-byte encoded environment limit",
				maxMCPEncodedEnvironmentBytes,
			)
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading env file: %w", err)
	}

	return entries, nil
}

func buildMCPStdioEnvironment(cfg config.MCPServerConfig) ([]string, error) {
	return buildMCPStdioEnvironmentForPlatform(cfg, runtime.GOOS)
}

func buildMCPStdioEnvironmentForPlatform(
	cfg config.MCPServerConfig,
	goos string,
) ([]string, error) {
	if len(cfg.Env) > maxMCPEnvironmentEntries {
		return nil, fmt.Errorf(
			"MCP config environment has too many entries: maximum is %d",
			maxMCPEnvironmentEntries,
		)
	}

	fileEntries := []mcpEnvironmentEntry(nil)
	if cfg.EnvFile != "" {
		var err error
		fileEntries, err = loadEnvFileEntries(cfg.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load env file %s: %w", cfg.EnvFile, err)
		}
	}

	configKeys := make([]string, 0, len(cfg.Env))
	for key := range cfg.Env {
		configKeys = append(configKeys, key)
	}
	sort.Slice(configKeys, func(i, j int) bool {
		left := mcpEnvironmentKey(configKeys[i], goos)
		right := mcpEnvironmentKey(configKeys[j], goos)
		if left == right {
			return configKeys[i] < configKeys[j]
		}
		return left < right
	})

	if goos == "windows" {
		seen := make(map[string]string, len(configKeys))
		for _, key := range configKeys {
			folded := mcpEnvironmentKey(key, goos)
			if previous, ok := seen[folded]; ok && previous != key {
				return nil, fmt.Errorf(
					"MCP config environment key %q conflicts with another key on Windows",
					key,
				)
			}
			seen[folded] = key
		}
	}

	merged := make(map[string]mcpEnvironmentEntry, len(fileEntries)+len(configKeys))
	merge := func(source string, entry mcpEnvironmentEntry) error {
		if err := validateMCPEnvironmentEntry(source, entry); err != nil {
			return err
		}
		key := mcpEnvironmentKey(entry.name, goos)
		if goos == "windows" {
			entry.name = key
		}
		merged[key] = entry
		return nil
	}

	for _, entry := range fileEntries {
		if err := merge("env_file", entry); err != nil {
			return nil, err
		}
	}
	for _, key := range configKeys {
		if err := merge("config", mcpEnvironmentEntry{name: key, value: cfg.Env[key]}); err != nil {
			return nil, err
		}
	}

	if len(merged) > maxMCPEnvironmentEntries {
		return nil, fmt.Errorf(
			"MCP explicit environment has too many entries: maximum is %d",
			maxMCPEnvironmentEntries,
		)
	}

	entries := make([]mcpEnvironmentEntry, 0, len(merged))
	for _, entry := range merged {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].name < entries[j].name
	})

	environment := make([]string, 0, len(entries))
	encodedBytes := 0
	for _, entry := range entries {
		encodedBytes += len(entry.name) + 1 + len(entry.value) + 1
		if encodedBytes > maxMCPEncodedEnvironmentBytes {
			return nil, fmt.Errorf(
				"MCP explicit environment exceeds the %d-byte encoded limit",
				maxMCPEncodedEnvironmentBytes,
			)
		}
		environment = append(environment, entry.name+"="+entry.value)
	}
	return environment, nil
}

func mcpEnvironmentKey(name, goos string) string {
	if goos == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func validateMCPEnvironmentEntry(source string, entry mcpEnvironmentEntry) error {
	keyError := func(problem string) error {
		return fmt.Errorf("MCP %s environment key %q %s", source, entry.name, problem)
	}
	if entry.name == "" {
		return keyError("is empty")
	}
	if !utf8.ValidString(entry.name) {
		return keyError("is not valid UTF-8")
	}
	if len(entry.name) > maxMCPEnvironmentNameBytes {
		return keyError(fmt.Sprintf("exceeds the %d-byte limit", maxMCPEnvironmentNameBytes))
	}
	if strings.ContainsAny(entry.name, "=\x00") {
		return keyError("contains an invalid character")
	}
	for index, character := range entry.name {
		if character == '_' || character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return keyError("is not a portable environment variable name")
	}
	if !utf8.ValidString(entry.value) || strings.IndexByte(entry.value, 0) >= 0 {
		return keyError("has an invalid value")
	}
	if len(entry.value) > maxMCPEnvironmentValueBytes {
		return keyError(fmt.Sprintf("has a value exceeding the %d-byte limit", maxMCPEnvironmentValueBytes))
	}
	return nil
}

// ServerConnection represents a connection to an MCP server
type ServerConnection struct {
	Name        string
	Config      config.MCPServerConfig
	Client      *mcp.Client
	Session     *mcp.ClientSession
	Tools       []*mcp.Tool
	reconnectMu sync.Mutex
}

// Manager manages multiple MCP server connections
type Manager struct {
	servers                         map[string]*ServerConnection
	runtimeEvents                   runtimeevents.Bus
	executionPolicy                 isolation.ExecutionPolicy
	connect                         managerServerConnector
	connectAdmissionMu              sync.Mutex
	connectWG                       sync.WaitGroup
	mu                              sync.RWMutex
	closed                          atomic.Bool // changed from bool to atomic.Bool to avoid TOCTOU race
	workflowAuthoringIdentitiesSafe atomic.Bool
	wg                              sync.WaitGroup // tracks in-flight CallTool calls
}

type managerServerConnector func(
	context.Context,
	string,
	config.MCPServerConfig,
	isolation.ExecutionPolicy,
) (*ServerConnection, error)

// ManagerOption configures an MCP manager.
type ManagerOption func(*Manager)

// WithRuntimeEvents injects the runtime event bus used for MCP observations.
func WithRuntimeEvents(eventBus runtimeevents.Bus) ManagerOption {
	return func(m *Manager) {
		m.runtimeEvents = eventBus
	}
}

// ServerEventPayload describes MCP server connection events.
type ServerEventPayload struct {
	Server    string `json:"server"`
	Type      string `json:"type,omitempty"`
	URL       string `json:"url,omitempty"`
	Command   string `json:"command,omitempty"`
	Tool      string `json:"tool,omitempty"`
	ToolCount int    `json:"tool_count,omitempty"`
	Error     string `json:"error,omitempty"`
}

// NewManager creates a new MCP manager
func NewManager(opts ...ManagerOption) *Manager {
	return newManager(
		isolation.NewExecutionPolicy(config.DefaultConfig().Isolation),
		opts...,
	)
}

// NewManagerWithExecutionPolicy constructs a manager from one caller-owned
// process-generation snapshot without capturing a throwaway default policy.
func NewManagerWithExecutionPolicy(
	policy isolation.ExecutionPolicy,
	opts ...ManagerOption,
) *Manager {
	return newManager(policy, opts...)
}

func newManager(
	policy isolation.ExecutionPolicy,
	opts ...ManagerOption,
) *Manager {
	m := &Manager{
		servers:         make(map[string]*ServerConnection),
		executionPolicy: policy,
		connect:         connectServerWithExecutionPolicy,
	}
	m.workflowAuthoringIdentitiesSafe.Store(true)
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// LoadFromConfig loads MCP servers from configuration
func (m *Manager) LoadFromConfig(ctx context.Context, cfg *config.Config) error {
	return m.LoadFromMCPConfig(ctx, cfg.Tools.MCP, cfg.WorkspacePath())
}

// LoadFromMCPConfig loads MCP servers from MCP configuration and workspace path.
// This is the minimal dependency version that doesn't require the full Config object.
func (m *Manager) LoadFromMCPConfig(
	ctx context.Context,
	mcpCfg config.MCPConfig,
	workspacePath string,
) error {
	if !mcpCfg.Enabled {
		logger.InfoCF("mcp", "MCP integration is disabled", nil)
		return nil
	}

	if len(mcpCfg.Servers) == 0 {
		logger.InfoCF("mcp", "No MCP servers configured", nil)
		return nil
	}

	logger.InfoCF("mcp", "Initializing MCP servers",
		map[string]any{
			"count": len(mcpCfg.Servers),
		})

	var wg sync.WaitGroup
	errs := make(chan error, len(mcpCfg.Servers))
	enabledCount := 0

	for name, serverCfg := range mcpCfg.Servers {
		if !serverCfg.Enabled {
			logger.DebugCF("mcp", "Skipping disabled server",
				map[string]any{
					"server": name,
				})
			continue
		}

		enabledCount++
		wg.Add(1)
		go func(name string, serverCfg config.MCPServerConfig, workspace string) {
			defer wg.Done()

			// Resolve relative envFile paths relative to workspace
			if serverCfg.EnvFile != "" && !filepath.IsAbs(serverCfg.EnvFile) {
				if workspace == "" {
					err := fmt.Errorf(
						"workspace path is empty while resolving relative envFile %q for server %s",
						serverCfg.EnvFile,
						name,
					)
					logger.ErrorCF("mcp", "Invalid MCP server configuration",
						map[string]any{
							"server":   name,
							"env_file": serverCfg.EnvFile,
							"error":    err.Error(),
						})
					errs <- err
					return
				}
				serverCfg.EnvFile = filepath.Join(workspace, serverCfg.EnvFile)
			}

			if err := m.ConnectServer(ctx, name, serverCfg); err != nil {
				logger.ErrorCF("mcp", "Failed to connect to MCP server",
					map[string]any{
						"server": name,
						"error":  err.Error(),
					})
				errs <- fmt.Errorf("failed to connect to server %s: %w", name, err)
			}
		}(name, serverCfg, workspacePath)
	}

	wg.Wait()
	close(errs)

	// Collect errors
	var allErrors []error
	for err := range errs {
		allErrors = append(allErrors, err)
	}

	connectedCount := len(m.GetServers())

	// If all enabled servers failed to connect, return aggregated error
	if enabledCount > 0 && connectedCount == 0 {
		logger.ErrorCF("mcp", "All MCP servers failed to connect",
			map[string]any{
				"failed": len(allErrors),
				"total":  enabledCount,
			})
		return errors.Join(allErrors...)
	}

	if len(allErrors) > 0 {
		logger.WarnCF("mcp", "Some MCP servers failed to connect",
			map[string]any{
				"failed":    len(allErrors),
				"connected": connectedCount,
				"total":     enabledCount,
			})
		// Don't fail completely if some servers successfully connected
	}

	logger.InfoCF("mcp", "MCP server initialization complete",
		map[string]any{
			"connected": connectedCount,
			"total":     enabledCount,
		})

	return nil
}

// ConnectServer connects to a single MCP server
func (m *Manager) ConnectServer(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
) error {
	if err := m.beginConnectAdmission(ctx); err != nil {
		return err
	}
	defer m.connectWG.Done()
	m.publishServerEvent(runtimeevents.KindMCPServerConnecting, name, cfg, 0, nil)
	conn, err := m.connect(ctx, name, cfg, m.executionPolicy)
	if err != nil {
		m.publishServerEvent(runtimeevents.KindMCPServerFailed, name, cfg, 0, err)
		return err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		_ = closeServerConnection(conn)
		return contextErr
	}

	m.mu.Lock()
	if contextErr := ctx.Err(); contextErr != nil {
		m.mu.Unlock()
		_ = closeServerConnection(conn)
		return contextErr
	}
	if m.closed.Load() {
		m.mu.Unlock()
		_ = closeServerConnection(conn)
		m.publishServerEvent(runtimeevents.KindMCPServerFailed, name, cfg, 0, fmt.Errorf("manager is closed"))
		return fmt.Errorf("manager is closed")
	}

	previous := m.servers[name]
	m.servers[name] = conn
	m.refreshWorkflowAuthoringIdentitySafetyLocked()
	m.mu.Unlock()
	if previous != nil && previous != conn {
		_ = closeServerConnection(previous)
	}
	for _, tool := range conn.Tools {
		toolName := ""
		if tool != nil {
			toolName = tool.Name
		}
		m.publishToolDiscovered(name, cfg, toolName)
	}
	m.publishServerEvent(runtimeevents.KindMCPServerConnected, name, cfg, len(conn.Tools), nil)
	return nil
}

func (m *Manager) beginConnectAdmission(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("manager is nil")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.connectAdmissionMu.Lock()
	defer m.connectAdmissionMu.Unlock()
	if m.closed.Load() {
		return fmt.Errorf("manager is closed")
	}
	m.connectWG.Add(1)
	return nil
}

func connectServer(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
) (*ServerConnection, error) {
	return connectServerWithExecutionPolicy(
		ctx,
		name,
		cfg,
		isolation.NewExecutionPolicy(config.DefaultConfig().Isolation),
	)
}

func connectServerWithExecutionPolicy(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
	policy isolation.ExecutionPolicy,
) (*ServerConnection, error) {
	return connectServerWithOAuthAndExecutionPolicy(ctx, name, cfg, nil, nil, policy)
}

// ConnectServerWithOAuth connects one remote MCP server with an interactive
// OAuth handler. It is intended for short-lived management/login probes; the
// normal runtime path uses credentials already stored by the launcher.
func ConnectServerWithOAuth(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
	oauthHandler mcpauth.OAuthHandler,
	httpClient *http.Client,
) (*ServerConnection, error) {
	return connectServerWithOAuthAndExecutionPolicy(
		ctx,
		name,
		cfg,
		oauthHandler,
		httpClient,
		isolation.NewExecutionPolicy(config.DefaultConfig().Isolation),
	)
}

func connectServerWithOAuthAndExecutionPolicy(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
	oauthHandler mcpauth.OAuthHandler,
	httpClient *http.Client,
	policy isolation.ExecutionPolicy,
) (*ServerConnection, error) {
	logger.InfoCF("mcp", "Connecting to MCP server",
		map[string]any{
			"server":     name,
			"command":    cfg.Command,
			"args_count": len(cfg.Args),
		})

	// Create client
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "picoclaw",
		Version: "1.0.0",
	}, nil)

	// Create transport based on configuration
	// Auto-detect transport type if not explicitly specified
	var transport mcp.Transport
	transportType := config.EffectiveMCPTransportType(cfg)
	if transportType == "" {
		return nil, fmt.Errorf("either URL or command must be provided")
	}

	switch transportType {
	case "sse", "http":
		if cfg.URL == "" {
			return nil, fmt.Errorf("URL is required for SSE/HTTP transport")
		}
		parsedURL, err := url.Parse(cfg.URL)
		if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
			return nil, fmt.Errorf("invalid MCP server URL %q", cfg.URL)
		}
		if !strings.EqualFold(parsedURL.Scheme, "http") &&
			!strings.EqualFold(parsedURL.Scheme, "https") {
			return nil, fmt.Errorf("MCP server URL must use HTTP or HTTPS")
		}
		if parsedURL.User != nil {
			return nil, fmt.Errorf("MCP server URL must not contain embedded credentials")
		}

		headers := cloneStringMap(cfg.Headers)
		if len(headers) > 0 && !isHTTPSOrLoopbackHTTP(cfg.URL) {
			return nil, fmt.Errorf(
				"MCP custom headers require HTTPS, except for loopback development servers",
			)
		}
		var storedTokenSource oauth2.TokenSource
		if oauthHandler == nil {
			headers, storedTokenSource, err = serverHTTPAuth(name, cfg)
			if err != nil {
				return nil, err
			}
		} else {
			for key := range headers {
				if strings.EqualFold(key, "Authorization") {
					delete(headers, key)
				}
			}
		}

		// Configure DisableStandaloneSSE based on transport type.
		// - "http": Streamable HTTP request-response mode. Disable the standalone
		//   SSE stream to avoid compatibility issues with servers that don't
		//   support the optional GET listener.
		// - "sse": Bidirectional mode. Enable the standalone SSE stream to receive
		//   server-initiated notifications (e.g., ToolListChangedNotification).
		// - Empty or auto-detected: Defaults to "sse" behavior (standalone SSE enabled).
		disableStandaloneSSE := transportType == "http"

		logger.DebugCF("mcp", "Using SSE/HTTP transport",
			map[string]any{
				"server":               name,
				"url":                  cfg.URL,
				"disableStandaloneSSE": disableStandaloneSSE,
			})

		sseTransport := &mcp.StreamableClientTransport{
			Endpoint:             cfg.URL,
			DisableStandaloneSSE: disableStandaloneSSE,
			OAuthHandler:         oauthHandler,
			HTTPClient:           newMCPRemoteHTTPClient(parsedURL, httpClient),
		}

		// Add custom headers if provided.
		if len(headers) > 0 || storedTokenSource != nil {
			client := *sseTransport.HTTPClient
			client.Transport = &headerTransport{
				base:         client.Transport,
				headers:      headers,
				tokenSource:  storedTokenSource,
				originScheme: parsedURL.Scheme,
				originHost:   parsedURL.Host,
			}
			sseTransport.HTTPClient = &client
			logger.DebugCF("mcp", "Added custom HTTP headers",
				map[string]any{
					"server":       name,
					"header_count": len(headers),
				})
		}

		transport = sseTransport
	case "stdio":
		if cfg.Auth != nil {
			switch strings.ToLower(strings.TrimSpace(cfg.Auth.Type)) {
			case "", "none":
			default:
				return nil, fmt.Errorf("MCP auth is only supported for remote HTTP or SSE servers")
			}
		}
		if cfg.Command == "" {
			return nil, fmt.Errorf("command is required for stdio transport")
		}
		logger.DebugCF("mcp", "Using stdio transport",
			map[string]any{
				"server":  name,
				"command": cfg.Command,
			})
		command, err := expandPolicyHomeCommandPath(cfg.Command, policy)
		if err != nil {
			return nil, err
		}
		// The manager/session owns a successful stdio server lifetime. The
		// connect/reconnect admission context must not kill it after publication.
		cmd := exec.Command(command, cfg.Args...)

		env, err := buildMCPStdioEnvironment(cfg)
		if err != nil {
			return nil, err
		}
		if cfg.EnvFile != "" {
			logger.DebugCF("mcp", "Loaded environment variables from file",
				map[string]any{
					"server":  name,
					"envFile": cfg.EnvFile,
				})
		}
		cmd.Env = env
		transport = &isolatedCommandTransport{Command: cmd, ExecutionPolicy: policy}
	default:
		return nil, fmt.Errorf(
			"unsupported transport type: %s (supported: stdio, sse, http, streamable-http)",
			transportType,
		)
	}

	// Connect to server
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	// Get server info
	initResult := session.InitializeResult()
	logger.InfoCF("mcp", "Connected to MCP server",
		map[string]any{
			"server":        name,
			"serverName":    initResult.ServerInfo.Name,
			"serverVersion": initResult.ServerInfo.Version,
			"protocol":      initResult.ProtocolVersion,
		})

	// List available tools if supported
	tools, err := listServerTools(ctx, name, session, initResult)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		_ = session.Close()
		return nil, err
	}

	return &ServerConnection{
		Name:    name,
		Config:  cfg,
		Client:  client,
		Session: session,
		Tools:   tools,
	}, nil
}

// GetServers returns all connected servers
func (m *Manager) GetServers() map[string]*ServerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ServerConnection, len(m.servers))
	for k, v := range m.servers {
		result[k] = v
	}
	return result
}

// GetServer returns a specific server connection
func (m *Manager) GetServer(name string) (*ServerConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conn, ok := m.servers[name]
	return conn, ok
}

// WorkflowAuthoringIdentitiesSafe reports whether distinct exact identities in
// the manager's current tool set have unique canonical names. It is maintained
// whenever a live connection changes, outside the catalog request path.
func (m *Manager) WorkflowAuthoringIdentitiesSafe() bool {
	return m != nil && m.workflowAuthoringIdentitiesSafe.Load()
}

func (m *Manager) refreshWorkflowAuthoringIdentitySafetyLocked() {
	seen := make(map[string]ToolIdentity)
	safe := true
	for serverName, connection := range m.servers {
		if connection == nil {
			continue
		}
		for _, tool := range connection.Tools {
			if tool == nil {
				continue
			}
			canonical := CanonicalToolName(serverName, tool.Name)
			identity := ToolIdentity{
				Server: serverName,
				Tool:   tool.Name,
			}
			if previous, duplicateOrCollision := seen[canonical]; duplicateOrCollision {
				if previous != identity {
					safe = false
				}
				continue
			}
			seen[canonical] = identity
		}
	}
	m.workflowAuthoringIdentitiesSafe.Store(safe)
}

// VisitWorkflowAuthoringServers visits one identity-safe live server snapshot
// without allocating a full map copy. The callback runs under the manager read
// lock and must not mutate the manager.
func (m *Manager) VisitWorkflowAuthoringServers(
	ctx context.Context,
	visit func(string, *ServerConnection) bool,
) (bool, error) {
	if m == nil || visit == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if m.closed.Load() || !m.workflowAuthoringIdentitiesSafe.Load() {
		return false, nil
	}
	for name, connection := range m.servers {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !visit(name, connection) {
			return true, ctx.Err()
		}
	}
	return true, ctx.Err()
}

// CallTool calls a tool on a specific server
func (m *Manager) CallTool(
	ctx context.Context,
	serverName, toolName string,
	arguments map[string]any,
) (*mcp.CallToolResult, error) {
	// Serialize the positive WaitGroup Add with Close's terminal cutoff. Once
	// closed is published under this gate, no call can increment the counter.
	m.connectAdmissionMu.Lock()
	if m.closed.Load() {
		m.connectAdmissionMu.Unlock()
		return nil, fmt.Errorf("manager is closed")
	}
	m.wg.Add(1)
	m.connectAdmissionMu.Unlock()
	defer m.wg.Done()

	m.mu.RLock()
	conn, ok := m.servers[serverName]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("server %s not found", serverName)
	}

	params := &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	}

	result, err := conn.Session.CallTool(ctx, params)
	if err != nil {
		if shouldReconnectCallError(err) {
			logger.WarnCF("mcp", "MCP server session was lost during tool call, reconnecting",
				map[string]any{
					"server": serverName,
					"tool":   toolName,
					"error":  err.Error(),
				})

			reconnectedConn, reconnectErr := m.reconnectServer(ctx, serverName, conn)
			if reconnectErr != nil {
				return nil, fmt.Errorf("failed to recover lost MCP session: %w", reconnectErr)
			}

			result, err = reconnectedConn.Session.CallTool(ctx, params)
			if err == nil {
				return result, nil
			}
		}

		return nil, fmt.Errorf("failed to call tool: %w", err)
	}

	return result, nil
}

func listServerTools(
	ctx context.Context,
	name string,
	session *mcp.ClientSession,
	initResult *mcp.InitializeResult,
) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	if initResult.Capabilities.Tools == nil {
		return tools, ctx.Err()
	}

	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			logger.WarnCF("mcp", "Error listing tool",
				map[string]any{
					"server": name,
					"error":  err.Error(),
				})
			continue
		}
		tools = append(tools, tool)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	logger.InfoCF("mcp", "Listed tools from MCP server",
		map[string]any{
			"server":    name,
			"toolCount": len(tools),
		})

	return tools, nil
}

func shouldReconnectCallError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mcp.ErrSessionMissing) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), mcp.ErrSessionMissing.Error())
}

func (m *Manager) reconnectServer(
	ctx context.Context,
	serverName string,
	staleConn *ServerConnection,
) (*ServerConnection, error) {
	if err := m.beginConnectAdmission(ctx); err != nil {
		return nil, err
	}
	defer m.connectWG.Done()
	if staleConn == nil {
		return nil, fmt.Errorf("server %s not found", serverName)
	}

	staleConn.reconnectMu.Lock()
	defer staleConn.reconnectMu.Unlock()

	if m.closed.Load() {
		return nil, fmt.Errorf("manager is closed")
	}

	m.mu.RLock()
	currentConn, ok := m.servers[serverName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("server %s not found", serverName)
	}
	if currentConn != staleConn {
		return currentConn, nil
	}

	freshConn, err := m.connect(ctx, serverName, staleConn.Config, m.executionPolicy)
	if err != nil {
		return nil, err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		_ = closeServerConnection(freshConn)
		return nil, contextErr
	}

	m.mu.Lock()
	if contextErr := ctx.Err(); contextErr != nil {
		m.mu.Unlock()
		_ = closeServerConnection(freshConn)
		return nil, contextErr
	}
	if m.closed.Load() {
		m.mu.Unlock()
		_ = closeServerConnection(freshConn)
		return nil, fmt.Errorf("manager is closed")
	}

	currentConn, ok = m.servers[serverName]
	if !ok {
		m.mu.Unlock()
		_ = closeServerConnection(freshConn)
		return nil, fmt.Errorf("server %s not found", serverName)
	}

	if currentConn == staleConn {
		m.workflowAuthoringIdentitiesSafe.Store(false)
		m.servers[serverName] = freshConn
		m.refreshWorkflowAuthoringIdentitySafetyLocked()
		staleToClose := staleConn
		m.mu.Unlock()
		_ = closeServerConnection(staleToClose)
		return freshConn, nil
	}

	m.mu.Unlock()
	_ = closeServerConnection(freshConn)
	return currentConn, nil
}

func closeServerConnection(connection *ServerConnection) error {
	if connection == nil || connection.Session == nil {
		return nil
	}
	return connection.Session.Close()
}

// Close closes all server connections
func (m *Manager) Close() error {
	m.connectAdmissionMu.Lock()
	if m.closed.Swap(true) {
		m.connectAdmissionMu.Unlock()
		return nil // already closed
	}
	m.connectAdmissionMu.Unlock()
	m.workflowAuthoringIdentitiesSafe.Store(false)

	// Join connection creation before taking the server map. No new connect or
	// reconnect admission can increment this wait group after closed publication.
	m.connectWG.Wait()
	// Wait for all in-flight CallTool calls to finish before closing sessions
	// After closed=true is set, no new CallTool can start (they check closed first)
	m.wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	logger.InfoCF("mcp", "Closing all MCP server connections",
		map[string]any{
			"count": len(m.servers),
		})

	var errs []error
	for name, conn := range m.servers {
		if err := conn.Session.Close(); err != nil {
			logger.ErrorCF("mcp", "Failed to close server connection",
				map[string]any{
					"server": name,
					"error":  err.Error(),
				})
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
		}
	}

	m.servers = make(map[string]*ServerConnection)
	m.workflowAuthoringIdentitiesSafe.Store(false)

	if len(errs) > 0 {
		return fmt.Errorf("failed to close %d server(s): %w", len(errs), errors.Join(errs...))
	}

	return nil
}

// GetAllTools returns all tools from all connected servers
func (m *Manager) GetAllTools() map[string][]*mcp.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]*mcp.Tool)
	for name, conn := range m.servers {
		if len(conn.Tools) > 0 {
			result[name] = conn.Tools
		}
	}
	return result
}
