package cliprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/sipeed/picoclaw/pkg/auth"
)

const (
	githubCopilotDefaultModel = "auto"
	githubCopilotClientName   = "picoclaw"
	githubCopilotSetupURL     = "https://github.com/github/copilot-sdk/blob/main/docs/getting-started.md"
	githubCopilotTokenURL     = "https://api.github.com/copilot_internal/v2/token"
	githubCopilotAPIBaseURL   = "https://api.githubcopilot.com"
)

type copilotClient interface {
	Start(ctx context.Context) error
	Stop()
	CreateSession(ctx context.Context, cfg *copilot.SessionConfig) (copilotSession, error)
}

type copilotSession interface {
	SendAndWait(ctx context.Context, opts copilot.MessageOptions) (*copilot.SessionEvent, error)
}

type sdkCopilotClient struct {
	client *copilot.Client
}

func newSDKCopilotClient(opts *copilot.ClientOptions) copilotClient {
	return &sdkCopilotClient{client: copilot.NewClient(opts)}
}

func (c *sdkCopilotClient) Start(ctx context.Context) error {
	return c.client.Start(ctx)
}

func (c *sdkCopilotClient) Stop() {
	c.client.Stop()
}

func (c *sdkCopilotClient) CreateSession(
	ctx context.Context,
	cfg *copilot.SessionConfig,
) (copilotSession, error) {
	return c.client.CreateSession(ctx, cfg)
}

var (
	newCopilotClient             = newSDKCopilotClient
	githubCopilotHTTPClient      = &http.Client{Timeout: 30 * time.Second}
	githubCopilotTokenEndpoint   = githubCopilotTokenURL
	githubCopilotAPIBaseEndpoint = githubCopilotAPIBaseURL
)

type GitHubCopilotProvider struct {
	uri         string
	connectMode string // "stdio" or "grpc"
	token       string
	model       string

	client  copilotClient
	session copilotSession

	mu sync.Mutex
}

func NewGitHubCopilotProvider(uri string, connectMode string, model string) (*GitHubCopilotProvider, error) {
	if connectMode == "" {
		connectMode = "grpc"
	}

	switch connectMode {
	case "stdio":
		// TODO: Implement stdio mode for GitHub Copilot provider
		// See https://github.com/github/copilot-sdk/blob/main/docs/getting-started.md for details
		return nil, fmt.Errorf("stdio mode not implemented for GitHub Copilot provider; please use 'grpc' mode instead")
	case "grpc":
		return newGitHubCopilotProvider(uri, connectMode, model, &copilot.ClientOptions{
			CLIUrl: uri,
		})
	default:
		return nil, fmt.Errorf("unknown connect mode: %s", connectMode)
	}
}

func NewGitHubCopilotProviderWithToken(token string, model string) (*GitHubCopilotProvider, error) {
	token = strings.TrimSpace(token)
	if err := auth.ValidateGitHubCopilotToken(token); err != nil {
		return nil, err
	}
	return &GitHubCopilotProvider{
		connectMode: "token",
		token:       token,
		model:       githubCopilotModelOrDefault(model),
	}, nil
}

func ListGitHubCopilotModelsWithToken(ctx context.Context, token string) ([]copilot.ModelInfo, error) {
	token = strings.TrimSpace(token)
	if err := auth.ValidateGitHubCopilotToken(token); err != nil {
		return nil, err
	}
	authInfo, err := resolveGitHubCopilotAPIAuth(ctx, token)
	if err != nil {
		var exchangeErr *githubCopilotTokenExchangeError
		if !errors.As(err, &exchangeErr) || exchangeErr.statusCode != http.StatusForbidden {
			return nil, err
		}

		models, fallbackErr := listGitHubCopilotModels(ctx, &githubCopilotAPIAuth{
			token:   token,
			apiBase: githubCopilotAPIBaseEndpoint,
		})
		if fallbackErr != nil {
			return nil, fmt.Errorf(
				"%w; raw-token model fallback failed: %w",
				err,
				fallbackErr,
			)
		}
		return models, nil
	}
	return listGitHubCopilotModels(ctx, authInfo)
}

func newGitHubCopilotProvider(
	uri string,
	connectMode string,
	model string,
	clientOptions *copilot.ClientOptions,
) (*GitHubCopilotProvider, error) {
	client := newCopilotClient(clientOptions)
	if err := client.Start(context.Background()); err != nil {
		return nil, fmt.Errorf(
			"can't connect to GitHub Copilot: %w; see %s for setup details",
			err,
			githubCopilotSetupURL,
		)
	}

	session, err := client.CreateSession(context.Background(), &copilot.SessionConfig{
		ClientName:          githubCopilotClientName,
		Model:               githubCopilotModelOrDefault(model),
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		Hooks:               &copilot.SessionHooks{},
	})
	if err != nil {
		client.Stop()
		return nil, fmt.Errorf("create session failed: %w", err)
	}

	return &GitHubCopilotProvider{
		uri:         uri,
		connectMode: connectMode,
		client:      client,
		session:     session,
	}, nil
}

func githubCopilotModelOrDefault(model string) string {
	if model = strings.TrimSpace(model); model != "" {
		return model
	}
	return githubCopilotDefaultModel
}

func (p *GitHubCopilotProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		p.client.Stop()
		p.client = nil
		p.session = nil
	}
}

func (p *GitHubCopilotProvider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	if p.connectMode == "token" {
		return p.chatWithToken(ctx, messages, model)
	}

	type tempMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	out := make([]tempMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, tempMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	fullcontent, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal messages: %w", err)
	}
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()

	if session == nil {
		return nil, fmt.Errorf("provider closed")
	}

	resp, err := session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: string(fullcontent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send message to copilot: %w", err)
	}

	if resp == nil {
		return nil, fmt.Errorf("empty response from copilot")
	}
	if resp.Data.Content == nil {
		return nil, fmt.Errorf("no content in copilot response")
	}
	content := *resp.Data.Content

	return &LLMResponse{
		FinishReason: "stop",
		Content:      content,
	}, nil
}

func (p *GitHubCopilotProvider) GetDefaultModel() string {
	return githubCopilotDefaultModel
}

type githubCopilotAPIAuth struct {
	token   string
	apiBase string
}

type githubCopilotTokenResponse struct {
	Token     string `json:"token"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

type githubCopilotTokenExchangeError struct {
	statusCode int
	err        error
}

func (e *githubCopilotTokenExchangeError) Error() string {
	return fmt.Sprintf("exchanging GitHub token for Copilot API token: %v", e.err)
}

func (e *githubCopilotTokenExchangeError) Unwrap() error {
	return e.err
}

func resolveGitHubCopilotAPIAuth(ctx context.Context, githubToken string) (*githubCopilotAPIAuth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubCopilotTokenEndpoint, nil)
	if err != nil {
		return nil, err
	}
	setGitHubCopilotHeaders(req)
	req.Header.Set("Authorization", "token "+strings.TrimSpace(githubToken))

	resp, err := githubCopilotHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchanging GitHub token for Copilot API token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &githubCopilotAPIAuth{
			token:   strings.TrimSpace(githubToken),
			apiBase: githubCopilotAPIBaseEndpoint,
		}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &githubCopilotTokenExchangeError{
			statusCode: resp.StatusCode,
			err: githubCopilotStatusError(
				"GitHub",
				resp.StatusCode,
				resp.Body,
				githubToken,
			),
		}
	}

	var body githubCopilotTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding Copilot API token response: %w", err)
	}
	copilotToken := strings.TrimSpace(body.Token)
	if copilotToken == "" {
		return nil, fmt.Errorf("GitHub token exchange returned no Copilot API token")
	}
	return &githubCopilotAPIAuth{
		token:   copilotToken,
		apiBase: normalizeGitHubCopilotAPIBase(body.Endpoints.API),
	}, nil
}

func normalizeGitHubCopilotAPIBase(apiBase string) string {
	apiBase = strings.TrimSpace(apiBase)
	if apiBase == "" {
		return githubCopilotAPIBaseEndpoint
	}
	if !strings.Contains(apiBase, "://") {
		apiBase = "https://" + apiBase
	}
	return strings.TrimRight(apiBase, "/")
}

func setGitHubCopilotHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	req.Header.Set("Editor-Version", "vscode/1.104.3")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.26.7")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Openai-Intent", "conversation-panel")
	req.Header.Set("X-Github-Api-Version", "2025-04-01")
}

type githubCopilotModelsResponse struct {
	Data   []copilot.ModelInfo `json:"data"`
	Models []copilot.ModelInfo `json:"models"`
}

func listGitHubCopilotModels(
	ctx context.Context,
	authInfo *githubCopilotAPIAuth,
) ([]copilot.ModelInfo, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		normalizeGitHubCopilotAPIBase(authInfo.apiBase)+"/models",
		nil,
	)
	if err != nil {
		return nil, err
	}
	setGitHubCopilotHeaders(req)
	req.Header.Set("Authorization", "Bearer "+authInfo.token)

	resp, err := githubCopilotHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting GitHub Copilot models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, githubCopilotStatusError(
			"GitHub Copilot models",
			resp.StatusCode,
			resp.Body,
			authInfo.token,
		)
	}

	var body githubCopilotModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding GitHub Copilot models: %w", err)
	}
	models := body.Data
	if len(models) == 0 {
		models = body.Models
	}
	return models, nil
}

func (p *GitHubCopilotProvider) chatWithToken(
	ctx context.Context,
	messages []Message,
	model string,
) (*LLMResponse, error) {
	authInfo, err := resolveGitHubCopilotAPIAuth(ctx, p.token)
	if err != nil {
		return nil, err
	}
	model = githubCopilotModelOrDefault(model)
	if model == githubCopilotDefaultModel {
		model = p.model
	}
	if model == githubCopilotDefaultModel {
		model = "gpt-4.1"
	}

	body := map[string]any{
		"model":    model,
		"messages": githubCopilotChatMessages(messages),
		"stream":   false,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal GitHub Copilot chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		normalizeGitHubCopilotAPIBase(authInfo.apiBase)+"/chat/completions",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, err
	}
	setGitHubCopilotHeaders(req)
	req.Header.Set("Authorization", "Bearer "+authInfo.token)

	resp, err := githubCopilotHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending GitHub Copilot chat request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, githubCopilotStatusError(
			"GitHub Copilot chat",
			resp.StatusCode,
			resp.Body,
			authInfo.token,
		)
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decoding GitHub Copilot chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("empty response from GitHub Copilot")
	}
	content := chatResp.Choices[0].Message.Content
	if content == "" {
		return nil, fmt.Errorf("no content in GitHub Copilot response")
	}
	finishReason := strings.TrimSpace(chatResp.Choices[0].FinishReason)
	if finishReason == "" {
		finishReason = "stop"
	}
	return &LLMResponse{FinishReason: finishReason, Content: content}, nil
}

func githubCopilotChatMessages(messages []Message) []map[string]string {
	out := make([]map[string]string, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		out = append(out, map[string]string{
			"role":    role,
			"content": msg.Content,
		})
	}
	return out
}

func githubCopilotStatusError(
	provider string,
	statusCode int,
	body io.Reader,
	secrets ...string,
) error {
	preview, _ := io.ReadAll(io.LimitReader(body, 512))
	detail := strings.TrimSpace(string(preview))
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); secret != "" {
			detail = strings.ReplaceAll(detail, secret, "[REDACTED]")
		}
	}
	if detail == "" {
		return fmt.Errorf("%s returned status %d", provider, statusCode)
	}
	return fmt.Errorf("%s returned status %d: %s", provider, statusCode, detail)
}
