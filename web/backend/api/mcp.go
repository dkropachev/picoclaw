package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
)

const mcpProbeTimeout = 15 * time.Second

var (
	mcpProbeServer          = defaultMCPProbeServer
	mcpSaveConfigIfRevision = config.SaveConfigIfRevision
)

type mcpConfigResponse struct {
	Enabled   bool                       `json:"enabled"`
	Discovery config.ToolDiscoveryConfig `json:"discovery"`
	Servers   []mcpServerSummary         `json:"servers"`
}

type mcpServerSummary struct {
	Name       string         `json:"name"`
	Enabled    bool           `json:"enabled"`
	Deferred   *bool          `json:"deferred"`
	Type       string         `json:"type"`
	URL        string         `json:"url"`
	Command    string         `json:"command"`
	Args       []string       `json:"args"`
	EnvFile    string         `json:"env_file"`
	EnvKeys    []string       `json:"env_keys"`
	HeaderKeys []string       `json:"header_keys"`
	Auth       mcpAuthSummary `json:"auth"`
}

type mcpAuthSummary struct {
	Type       string `json:"type"`
	Configured bool   `json:"configured"`
	Expired    bool   `json:"expired,omitempty"`
}

type mcpSettingsRequest struct {
	Enabled   bool                       `json:"enabled"`
	Discovery config.ToolDiscoveryConfig `json:"discovery"`
}

type mcpServerRequest struct {
	ExpectedConfigRevision string            `json:"expected_config_revision,omitempty"`
	Name                   string            `json:"name"`
	Enabled                *bool             `json:"enabled"`
	Deferred               *bool             `json:"deferred"`
	Type                   string            `json:"type"`
	URL                    string            `json:"url"`
	Command                string            `json:"command"`
	Args                   []string          `json:"args"`
	Env                    map[string]string `json:"env"`
	EnvKeys                *[]string         `json:"env_keys"`
	EnvFile                string            `json:"env_file"`
	Headers                map[string]string `json:"headers"`
	HeaderKeys             *[]string         `json:"header_keys"`
	AuthMode               string            `json:"auth_mode"`
}

type mcpProbeRequest struct {
	Name   string           `json:"name"`
	Server mcpServerRequest `json:"server"`
}

type mcpProbeTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type mcpProbeResponse struct {
	OK           bool           `json:"ok"`
	ToolCount    int            `json:"tool_count"`
	Tools        []mcpProbeTool `json:"tools"`
	Error        string         `json:"error,omitempty"`
	AuthRequired bool           `json:"auth_required,omitempty"`
}

type mcpCredentialRequest struct {
	Token    string `json:"token"`
	AuthType string `json:"auth_type"`
}

func (h *Handler) registerMCPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp", h.handleGetMCP)
	mux.HandleFunc("PATCH /api/mcp/settings", h.requireMCPMutationOrigin(h.handleUpdateMCPSettings))
	mux.HandleFunc("GET /api/mcp/servers", h.handleListMCPServers)
	mux.HandleFunc("POST /api/mcp/servers", h.requireMCPMutationOrigin(h.handleAddMCPServer))
	mux.HandleFunc("POST /api/mcp/servers/bulk-delete", h.requireMCPMutationOrigin(h.handleBulkDeleteMCPServers))
	mux.HandleFunc("GET /api/mcp/servers/{name}", h.handleGetMCPServer)
	mux.HandleFunc("PUT /api/mcp/servers/{name}", h.requireMCPMutationOrigin(h.handleUpdateMCPServer))
	mux.HandleFunc("DELETE /api/mcp/servers/{name}", h.requireMCPMutationOrigin(h.handleDeleteMCPServer))
	mux.HandleFunc("POST /api/mcp/servers/test", h.requireMCPMutationOrigin(h.handleTestMCPServer))
	mux.HandleFunc(
		"PUT /api/mcp/servers/{name}/credential",
		h.requireMCPMutationOrigin(h.handleSetMCPServerCredential),
	)
	mux.HandleFunc(
		"DELETE /api/mcp/servers/{name}/credential",
		h.requireMCPMutationOrigin(h.handleDeleteMCPServerCredential),
	)
	h.registerMCPOAuthRoutes(mux)
}

func (h *Handler) requireMCPMutationOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
		case "", "none", "same-origin":
		default:
			http.Error(w, "Cross-origin MCP mutations are not allowed", http.StatusForbidden)
			return
		}
		if rawOrigin := strings.TrimSpace(r.Header.Get("Origin")); rawOrigin != "" {
			origin, err := url.Parse(rawOrigin)
			if err != nil || origin.Scheme == "" || origin.Host == "" ||
				!strings.EqualFold(origin.Host, mcpRequestHost(h, r)) ||
				!strings.EqualFold(origin.Scheme, mcpRequestScheme(h, r)) {
				http.Error(w, "Cross-origin MCP mutations are not allowed", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func mcpRequestHost(h *Handler, r *http.Request) string {
	if h.trustMCPOAuthForwardedHeaders(r) {
		if forwarded := forwardedHostFirst(r); forwarded != "" {
			return forwarded
		}
	}
	return r.Host
}

func mcpRequestScheme(h *Handler, r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if h.trustMCPOAuthForwardedHeaders(r) {
		if forwarded := forwardedProtoFirst(r); forwarded == "http" || forwarded == "https" {
			return forwarded
		}
	}
	return "http"
}

func (h *Handler) handleGetMCP(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	response, err := buildMCPConfigResponse(cfg)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load MCP credentials: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, response)
}

func (h *Handler) handleUpdateMCPSettings(w http.ResponseWriter, r *http.Request) {
	var request mcpSettingsRequest
	if err := decodeMCPJSON(r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateMCPDiscovery(request.Enabled, request.Discovery); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	unlock := h.lockMCPConfigMutation()
	defer unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	cfg.Tools.MCP.Enabled = request.Enabled
	cfg.Tools.MCP.Discovery = request.Discovery
	if _, err := mcpSaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		writeMCPConfigSaveError(w, err)
		return
	}
	writeMCPConfigResponse(w, cfg)
}

func (h *Handler) handleAddMCPServer(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request mcpServerRequest
	if err := decodeMCPJSON(r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	unlock := h.lockMCPConfigMutation()
	defer unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, request.ExpectedConfigRevision)
	if !ok || expectedRevision != "" && !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	if findMCPServerName(cfg.Tools.MCP.Servers, request.Name) != "" {
		http.Error(w, fmt.Sprintf("MCP server %q already exists", strings.TrimSpace(request.Name)), http.StatusConflict)
		return
	}
	server, err := buildMCPServerConfig(request, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateMCPRemoteSecretsTransport(server, request.AuthMode); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(request.Name)
	if cfg.Tools.MCP.Servers == nil {
		cfg.Tools.MCP.Servers = make(map[string]config.MCPServerConfig)
	}
	firstServer := len(cfg.Tools.MCP.Servers) == 0
	cfg.Tools.MCP.Servers[name] = server
	if firstServer {
		cfg.Tools.MCP.Enabled = true
	}
	if _, err := mcpSaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		writeMCPConfigSaveError(w, err)
		return
	}
	writeMCPConfigResponse(w, cfg)
}

func (h *Handler) handleUpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request mcpServerRequest
	if err := decodeMCPJSON(r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	unlock := h.lockMCPConfigMutation()
	defer unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, request.ExpectedConfigRevision)
	if !ok || expectedRevision != "" && !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	oldName := r.PathValue("name")
	actualOldName := findMCPServerName(cfg.Tools.MCP.Servers, oldName)
	if actualOldName == "" {
		http.Error(w, fmt.Sprintf("MCP server %q not found", oldName), http.StatusNotFound)
		return
	}
	newName := strings.TrimSpace(request.Name)
	if collision := findMCPServerName(cfg.Tools.MCP.Servers, newName); collision != "" &&
		!strings.EqualFold(collision, actualOldName) {
		http.Error(w, fmt.Sprintf("MCP server %q already exists", newName), http.StatusConflict)
		return
	}

	existing := cfg.Tools.MCP.Servers[actualOldName]
	previousCredentialID := ""
	if existing.Auth != nil {
		previousCredentialID, _ = picomcp.CredentialID(actualOldName, existing.Auth)
	}
	server, err := buildMCPServerConfig(request, &existing)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	server, _, err = protectMCPServerOriginChange(existing, server, request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateMCPRemoteSecretsTransport(server, request.AuthMode); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if newName != actualOldName && server.Auth != nil && strings.TrimSpace(server.Auth.CredentialID) == "" {
		credentialID, credentialErr := picomcp.CredentialID(actualOldName, server.Auth)
		if credentialErr != nil {
			http.Error(w, credentialErr.Error(), http.StatusBadRequest)
			return
		}
		authCopy := *server.Auth
		authCopy.CredentialID = credentialID
		server.Auth = &authCopy
	}

	delete(cfg.Tools.MCP.Servers, actualOldName)
	cfg.Tools.MCP.Servers[newName] = server
	if _, err := mcpSaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		writeMCPConfigSaveError(w, err)
		return
	}
	currentCredentialID := ""
	if server.Auth != nil {
		currentCredentialID, _ = picomcp.CredentialID(newName, server.Auth)
	}
	if previousCredentialID != "" && previousCredentialID != currentCredentialID &&
		!mcpCredentialReferenced(cfg.Tools.MCP.Servers, previousCredentialID) {
		_ = picoauth.DeleteCredential(previousCredentialID)
	}
	writeMCPConfigResponse(w, cfg)
}

func (h *Handler) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	unlock := h.lockMCPConfigMutation()
	defer unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, "")
	if !ok || expectedRevision != "" && !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	name := findMCPServerName(cfg.Tools.MCP.Servers, r.PathValue("name"))
	if name == "" {
		http.Error(w, fmt.Sprintf("MCP server %q not found", r.PathValue("name")), http.StatusNotFound)
		return
	}
	if blockers := mcpServerReferences(cfg, name); len(blockers) > 0 {
		writeCollectionError(w, http.StatusConflict, "mcp_server_referenced", "MCP server is still referenced", -1, blockers)
		return
	}
	server := cfg.Tools.MCP.Servers[name]
	credentialID, _ := picomcp.CredentialID(name, server.Auth)
	delete(cfg.Tools.MCP.Servers, name)
	if len(cfg.Tools.MCP.Servers) == 0 {
		cfg.Tools.MCP.Enabled = false
	}
	if _, err := mcpSaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		writeMCPConfigSaveError(w, err)
		return
	}
	if server.Auth != nil && credentialID != "" && !mcpCredentialReferenced(cfg.Tools.MCP.Servers, credentialID) {
		_ = picoauth.DeleteCredential(credentialID)
	}
	writeCollectionJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleTestMCPServer(w http.ResponseWriter, r *http.Request) {
	var request mcpProbeRequest
	if err := decodeMCPJSON(r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	var existing *config.MCPServerConfig
	if currentName := findMCPServerName(cfg.Tools.MCP.Servers, request.Name); currentName != "" {
		current := cfg.Tools.MCP.Servers[currentName]
		existing = &current
	}
	server, err := buildMCPServerConfig(request.Server, existing)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if existing != nil {
		server, _, err = protectMCPServerOriginChange(*existing, server, request.Server)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := validateMCPRemoteSecretsTransport(server, request.Server.AuthMode); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	server.Enabled = true
	name := strings.TrimSpace(request.Server.Name)

	ctx, cancel := context.WithTimeout(r.Context(), mcpProbeTimeout)
	defer cancel()
	result, probeErr := mcpProbeServer(ctx, name, server, cfg.WorkspacePath())
	if probeErr != nil {
		message := probeErr.Error()
		writeJSON(w, mcpProbeResponse{
			OK:           false,
			ToolCount:    0,
			Tools:        []mcpProbeTool{},
			Error:        message,
			AuthRequired: isMCPAuthRequiredError(message),
		})
		return
	}
	writeJSON(w, result)
}

func (h *Handler) handleSetMCPServerCredential(w http.ResponseWriter, r *http.Request) {
	var request mcpCredentialRequest
	if err := decodeMCPJSON(r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.ToLower(strings.TrimSpace(request.AuthType)) != "bearer" {
		http.Error(w, "auth_type must be bearer", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(request.Token)
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	unlock := h.lockMCPConfigMutation()
	defer unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	name := findMCPServerName(cfg.Tools.MCP.Servers, r.PathValue("name"))
	if name == "" {
		http.Error(w, fmt.Sprintf("MCP server %q not found", r.PathValue("name")), http.StatusNotFound)
		return
	}
	server := cfg.Tools.MCP.Servers[name]
	if transport := config.EffectiveMCPTransportType(server); transport != "http" && transport != "sse" {
		http.Error(w, "credentials are only supported for remote MCP servers", http.StatusBadRequest)
		return
	}
	if !picomcp.IsHTTPSOrLoopbackHTTP(server.URL) {
		http.Error(
			w,
			"bearer credentials require HTTPS, except for loopback development servers",
			http.StatusBadRequest,
		)
		return
	}

	credentialID, err := credentialIDForMCPMutation(name, server.Auth, cfg.Tools.MCP.Servers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	oldCredential, err := picoauth.GetCredential(credentialID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load credential: %v", err), http.StatusInternalServerError)
		return
	}
	if err := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		AccessToken: token,
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save credential: %v", err), http.StatusInternalServerError)
		return
	}

	authRevision := nextMCPAuthRevision(server.Auth)
	server.Auth = &config.MCPServerAuthConfig{
		Type:         "bearer",
		CredentialID: credentialID,
		Revision:     authRevision,
	}
	cfg.Tools.MCP.Servers[name] = server
	if _, err := mcpSaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		if oldCredential == nil {
			_ = picoauth.DeleteCredential(credentialID)
		} else {
			_ = picoauth.SetCredential(credentialID, oldCredential)
		}
		writeMCPConfigSaveError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) handleDeleteMCPServerCredential(w http.ResponseWriter, r *http.Request) {
	unlock := h.lockMCPConfigMutation()
	defer unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	name := findMCPServerName(cfg.Tools.MCP.Servers, r.PathValue("name"))
	if name == "" {
		http.Error(w, fmt.Sprintf("MCP server %q not found", r.PathValue("name")), http.StatusNotFound)
		return
	}
	server := cfg.Tools.MCP.Servers[name]
	if server.Auth == nil {
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	credentialID, _ := picomcp.CredentialID(name, server.Auth)
	server.Auth = nil
	cfg.Tools.MCP.Servers[name] = server
	if _, err := mcpSaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		writeMCPConfigSaveError(w, err)
		return
	}
	if credentialID != "" && !mcpCredentialReferenced(cfg.Tools.MCP.Servers, credentialID) {
		if err := picoauth.DeleteCredential(credentialID); err != nil {
			http.Error(
				w,
				fmt.Sprintf("Credential disconnected but cleanup failed: %v", err),
				http.StatusInternalServerError,
			)
			return
		}
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// lockMCPConfigMutation keeps the broad config lock first in the lock order.
// MCP config/credential transactions then retain mcpMu's serialization, while
// OAuth persistence may safely take mcpOAuthMu last.
func (h *Handler) lockMCPConfigMutation() func() {
	h.configMutationMu.Lock()
	h.mcpMu.Lock()
	return func() {
		h.mcpMu.Unlock()
		h.configMutationMu.Unlock()
	}
}

func writeMCPConfigSaveError(w http.ResponseWriter, err error) {
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		http.Error(w, "Configuration changed; reload and try again", http.StatusConflict)
		return
	}
	http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
}

func buildMCPConfigResponse(cfg *config.Config) (mcpConfigResponse, error) {
	response := mcpConfigResponse{
		Enabled:   cfg.Tools.MCP.Enabled,
		Discovery: cfg.Tools.MCP.Discovery,
		Servers:   make([]mcpServerSummary, 0, len(cfg.Tools.MCP.Servers)),
	}
	names := make([]string, 0, len(cfg.Tools.MCP.Servers))
	for name := range cfg.Tools.MCP.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		server := cfg.Tools.MCP.Servers[name]
		authSummary := mcpAuthSummary{Type: "none"}
		if len(server.Headers) > 0 {
			authSummary = mcpAuthSummary{Type: "custom", Configured: true}
		}
		if server.Auth != nil {
			authType := strings.ToLower(strings.TrimSpace(server.Auth.Type))
			if authType != "" && authType != "none" {
				authSummary.Type = authType
				credentialID, err := picomcp.CredentialID(name, server.Auth)
				if err != nil {
					return mcpConfigResponse{}, err
				}
				credential, err := picoauth.GetCredential(credentialID)
				if err != nil {
					return mcpConfigResponse{}, err
				}
				authSummary.Configured = credential != nil && strings.TrimSpace(credential.AccessToken) != ""
				refreshable := credential != nil && authType == "oauth" &&
					strings.TrimSpace(credential.RefreshToken) != "" &&
					strings.TrimSpace(credential.OAuthTokenURL) != "" &&
					strings.TrimSpace(credential.OAuthClientID) != ""
				authSummary.Expired = authSummary.Configured && credential.IsExpired() && !refreshable
			}
		}
		response.Servers = append(response.Servers, mcpServerSummary{
			Name:       name,
			Enabled:    server.Enabled,
			Deferred:   cloneBoolPointer(server.Deferred),
			Type:       config.EffectiveMCPTransportType(server),
			URL:        server.URL,
			Command:    server.Command,
			Args:       append([]string{}, server.Args...),
			EnvFile:    server.EnvFile,
			EnvKeys:    sortedStringMapKeys(server.Env),
			HeaderKeys: sortedStringMapKeys(server.Headers),
			Auth:       authSummary,
		})
	}
	return response, nil
}

func buildMCPServerConfig(
	request mcpServerRequest,
	existing *config.MCPServerConfig,
) (config.MCPServerConfig, error) {
	name := strings.TrimSpace(request.Name)
	if err := validateMCPServerName(name); err != nil {
		return config.MCPServerConfig{}, err
	}
	transport := config.NormalizeMCPTransportType(request.Type)
	if transport == "" && existing != nil {
		transport = config.EffectiveMCPTransportType(*existing)
	}
	if transport != "stdio" && transport != "http" && transport != "sse" {
		return config.MCPServerConfig{}, fmt.Errorf("type must be stdio, http, or sse")
	}

	enabled := true
	if existing != nil {
		enabled = existing.Enabled
	}
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	server := config.MCPServerConfig{
		Enabled:  enabled,
		Deferred: cloneBoolPointer(request.Deferred),
		Type:     transport,
	}
	if existing != nil {
		server.Auth = cloneMCPAuthConfig(existing.Auth)
	}
	authMode := strings.ToLower(strings.TrimSpace(request.AuthMode))
	switch authMode {
	case "", "bearer", "oauth":
	case "none", "custom":
		server.Auth = nil
	default:
		return config.MCPServerConfig{}, fmt.Errorf(
			"auth_mode must be none, oauth, bearer, or custom",
		)
	}

	switch transport {
	case "stdio":
		if authMode != "" && authMode != "none" {
			return config.MCPServerConfig{}, fmt.Errorf(
				"stdio MCP servers do not support remote authentication",
			)
		}
		command := strings.TrimSpace(request.Command)
		if command == "" {
			return config.MCPServerConfig{}, fmt.Errorf("command is required for stdio MCP servers")
		}
		env, err := resolveMCPStringMap(existingMap(existing, true), request.Env, request.EnvKeys, false)
		if err != nil {
			return config.MCPServerConfig{}, fmt.Errorf("invalid env: %w", err)
		}
		server.Command = command
		server.Args = append([]string(nil), request.Args...)
		server.Env = env
		server.EnvFile = strings.TrimSpace(request.EnvFile)
		server.Auth = nil
	case "http", "sse":
		rawURL := strings.TrimSpace(request.URL)
		parsedURL, err := url.ParseRequestURI(rawURL)
		if err != nil || parsedURL.Host == "" ||
			(!strings.EqualFold(parsedURL.Scheme, "http") &&
				!strings.EqualFold(parsedURL.Scheme, "https")) {
			return config.MCPServerConfig{}, fmt.Errorf("a valid HTTP(S) URL is required for remote MCP servers")
		}
		if parsedURL.User != nil {
			return config.MCPServerConfig{}, fmt.Errorf("credentials must not be embedded in the MCP server URL")
		}
		headers, err := resolveMCPStringMap(existingMap(existing, false), request.Headers, request.HeaderKeys, true)
		if err != nil {
			return config.MCPServerConfig{}, fmt.Errorf("invalid headers: %w", err)
		}
		for key := range headers {
			if isReservedMCPHeader(key) {
				return config.MCPServerConfig{}, fmt.Errorf("header %q is managed by the MCP transport", key)
			}
		}
		server.URL = rawURL
		server.Headers = headers
	}
	return server, nil
}

func validateMCPServerName(name string) error {
	if name == "" {
		return fmt.Errorf("server name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("server name must be 64 characters or fewer")
	}
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '.', char == '_', char == '-':
		default:
			return fmt.Errorf("server name may contain only letters, numbers, dots, underscores, and hyphens")
		}
	}
	return nil
}

func validateMCPDiscovery(enabled bool, discovery config.ToolDiscoveryConfig) error {
	if !enabled || !discovery.Enabled {
		return nil
	}
	if discovery.TTL < 1 {
		return fmt.Errorf("discovery ttl must be at least 1")
	}
	if discovery.MaxSearchResults < 1 {
		return fmt.Errorf("discovery max_search_results must be at least 1")
	}
	if !discovery.UseBM25 && !discovery.UseRegex {
		return fmt.Errorf("discovery requires BM25 or regex search")
	}
	return nil
}

func resolveMCPStringMap(
	current map[string]string,
	values map[string]string,
	keys *[]string,
	caseInsensitive bool,
) (map[string]string, error) {
	if keys == nil && values == nil {
		return cloneMCPStringMap(current), nil
	}
	requestedKeys := make([]string, 0)
	if keys != nil {
		requestedKeys = append(requestedKeys, (*keys)...)
	} else {
		for key := range values {
			requestedKeys = append(requestedKeys, key)
		}
	}
	result := make(map[string]string, len(requestedKeys))
	seen := make(map[string]bool, len(requestedKeys))
	for _, rawKey := range requestedKeys {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return nil, fmt.Errorf("keys must not be empty")
		}
		comparisonKey := key
		if caseInsensitive {
			comparisonKey = strings.ToLower(key)
		}
		if seen[comparisonKey] {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		seen[comparisonKey] = true

		value, submitted := lookupMCPMapValue(values, key, caseInsensitive)
		if !submitted || value == "" {
			var configured bool
			value, configured = lookupMCPMapValue(current, key, caseInsensitive)
			if !configured {
				return nil, fmt.Errorf("value is required for new key %q", key)
			}
		}
		if strings.ContainsAny(value, "\r\n\x00") && caseInsensitive {
			return nil, fmt.Errorf("header %q contains an invalid value", key)
		}
		if caseInsensitive && !validMCPHeaderName(key) {
			return nil, fmt.Errorf("header name %q is invalid", key)
		}
		if !caseInsensitive && (strings.ContainsRune(key, '=') || strings.ContainsRune(key, '\x00') ||
			strings.ContainsRune(value, '\x00')) {
			return nil, fmt.Errorf("environment entry %q is invalid", key)
		}
		result[key] = value
	}
	return result, nil
}

func freshMCPStringMap(
	values map[string]string,
	keys *[]string,
	caseInsensitive bool,
) (map[string]string, error) {
	requestedKeys := make([]string, 0)
	if keys != nil {
		requestedKeys = append(requestedKeys, (*keys)...)
	} else {
		for key := range values {
			requestedKeys = append(requestedKeys, key)
		}
	}
	result := make(map[string]string, len(requestedKeys))
	seen := make(map[string]bool, len(requestedKeys))
	for _, rawKey := range requestedKeys {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return nil, fmt.Errorf("keys must not be empty")
		}
		comparisonKey := key
		if caseInsensitive {
			comparisonKey = strings.ToLower(key)
		}
		if seen[comparisonKey] {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		seen[comparisonKey] = true

		value, submitted := lookupMCPMapValue(values, key, caseInsensitive)
		if !submitted || value == "" {
			continue
		}
		if caseInsensitive && !validMCPHeaderName(key) {
			return nil, fmt.Errorf("header name %q is invalid", key)
		}
		if caseInsensitive && strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("header %q contains an invalid value", key)
		}
		result[key] = value
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func lookupMCPMapValue(values map[string]string, key string, caseInsensitive bool) (string, bool) {
	if !caseInsensitive {
		value, ok := values[key]
		return value, ok
	}
	for candidate, value := range values {
		if strings.EqualFold(candidate, key) {
			return value, true
		}
	}
	return "", false
}

func existingMap(existing *config.MCPServerConfig, env bool) map[string]string {
	if existing == nil {
		return nil
	}
	if env {
		return existing.Env
	}
	return existing.Headers
}

func protectMCPServerOriginChange(
	existing config.MCPServerConfig,
	candidate config.MCPServerConfig,
	request mcpServerRequest,
) (config.MCPServerConfig, bool, error) {
	existingTransport := config.EffectiveMCPTransportType(existing)
	candidateTransport := config.EffectiveMCPTransportType(candidate)
	changed := (existingTransport == "http" || existingTransport == "sse") &&
		(candidateTransport != "http" && candidateTransport != "sse" ||
			!sameMCPRemoteOrigin(existing.URL, candidate.URL))
	if !changed {
		return candidate, false, nil
	}

	// Never move an existing token or preserved secret header to another
	// origin as a side effect of editing or probing a server definition.
	candidate.Auth = nil
	if candidateTransport == "http" || candidateTransport == "sse" {
		headers, err := freshMCPStringMap(request.Headers, request.HeaderKeys, true)
		if err != nil {
			return config.MCPServerConfig{}, false, fmt.Errorf("invalid headers: %w", err)
		}
		candidate.Headers = headers
	}
	return candidate, true, nil
}

func validateMCPRemoteSecretsTransport(
	server config.MCPServerConfig,
	requestedAuthMode string,
) error {
	transport := config.EffectiveMCPTransportType(server)
	if transport != "http" && transport != "sse" {
		return nil
	}
	authType := ""
	if server.Auth != nil {
		authType = strings.ToLower(strings.TrimSpace(server.Auth.Type))
	}
	requestedAuthMode = strings.ToLower(strings.TrimSpace(requestedAuthMode))
	hasSecrets := (authType != "" && authType != "none") ||
		len(server.Headers) > 0 ||
		requestedAuthMode == "bearer" ||
		requestedAuthMode == "oauth" ||
		requestedAuthMode == "custom"
	if hasSecrets && !picomcp.IsHTTPSOrLoopbackHTTP(server.URL) {
		return fmt.Errorf(
			"MCP credentials and custom headers require HTTPS, except for loopback development servers",
		)
	}
	return nil
}

func sameMCPRemoteOrigin(left, right string) bool {
	leftURL, leftErr := url.Parse(strings.TrimSpace(left))
	rightURL, rightErr := url.Parse(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil || leftURL.Host == "" || rightURL.Host == "" {
		return false
	}
	leftPort := leftURL.Port()
	if leftPort == "" {
		leftPort = defaultMCPPort(leftURL.Scheme)
	}
	rightPort := rightURL.Port()
	if rightPort == "" {
		rightPort = defaultMCPPort(rightURL.Scheme)
	}
	return strings.EqualFold(leftURL.Scheme, rightURL.Scheme) &&
		strings.EqualFold(leftURL.Hostname(), rightURL.Hostname()) &&
		leftPort == rightPort
}

func defaultMCPPort(scheme string) string {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func defaultMCPProbeServer(
	ctx context.Context,
	name string,
	server config.MCPServerConfig,
	workspace string,
) (mcpProbeResponse, error) {
	manager := picomcp.NewManager()
	defer manager.Close()
	mcpConfig := config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers:    map[string]config.MCPServerConfig{name: server},
	}
	if err := manager.LoadFromMCPConfig(ctx, mcpConfig, workspace); err != nil {
		return mcpProbeResponse{}, err
	}
	connection, ok := manager.GetServer(name)
	if !ok {
		return mcpProbeResponse{}, fmt.Errorf("MCP server did not establish a connection")
	}
	tools := make([]mcpProbeTool, 0, len(connection.Tools))
	for _, tool := range connection.Tools {
		if tool != nil {
			tools = append(tools, mcpProbeTool{Name: tool.Name, Description: tool.Description})
		}
	}
	return mcpProbeResponse{OK: true, ToolCount: len(tools), Tools: tools}, nil
}

func credentialIDForMCPMutation(
	name string,
	authConfig *config.MCPServerAuthConfig,
	servers map[string]config.MCPServerConfig,
) (string, error) {
	credentialID, err := picomcp.CredentialID(name, authConfig)
	if err != nil {
		return "", err
	}
	if mcpCredentialReferencedByOtherServer(servers, name, credentialID) {
		return newMCPAuthCredentialID(name)
	}
	return credentialID, nil
}

func newMCPAuthCredentialID(name string) (string, error) {
	randomBytes := make([]byte, 6)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate credential id: %w", err)
	}
	return picoauth.NormalizeCredentialID("mcp", name+"-"+hex.EncodeToString(randomBytes))
}

func nextMCPAuthRevision(current *config.MCPServerAuthConfig) int64 {
	now := time.Now().UnixNano()
	if current != nil && now <= current.Revision {
		return current.Revision + 1
	}
	return now
}

func mcpCredentialReferenced(
	servers map[string]config.MCPServerConfig,
	credentialID string,
) bool {
	for name, server := range servers {
		if server.Auth == nil {
			continue
		}
		currentID, err := picomcp.CredentialID(name, server.Auth)
		if err == nil && currentID == credentialID {
			return true
		}
	}
	return false
}

func mcpCredentialReferencedByOtherServer(
	servers map[string]config.MCPServerConfig,
	excludedName string,
	credentialID string,
) bool {
	for name, server := range servers {
		if strings.EqualFold(name, excludedName) || server.Auth == nil {
			continue
		}
		currentID, err := picomcp.CredentialID(name, server.Auth)
		if err == nil && currentID == credentialID {
			return true
		}
	}
	return false
}

func findMCPServerName(servers map[string]config.MCPServerConfig, requested string) string {
	requested = strings.TrimSpace(requested)
	for name := range servers {
		if strings.EqualFold(name, requested) {
			return name
		}
	}
	return ""
}

func isMCPAuthRequiredError(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "forbidden") ||
		strings.Contains(message, "401") ||
		strings.Contains(message, "403") ||
		strings.Contains(message, "credential") ||
		strings.Contains(message, "oauth")
}

func isReservedMCPHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "accept", "content-length", "content-type", "host", "mcp-protocol-version", "mcp-session-id":
		return true
	default:
		return false
	}
}

func validMCPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", char):
		default:
			return false
		}
	}
	return true
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneMCPStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneMCPAuthConfig(value *config.MCPServerAuthConfig) *config.MCPServerAuthConfig {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func decodeMCPJSON(r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("Invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("Invalid JSON: request body must contain one object")
	}
	return nil
}

func writeMCPConfigResponse(w http.ResponseWriter, cfg *config.Config) {
	response, err := buildMCPConfigResponse(cfg)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load MCP credentials: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, response)
}
