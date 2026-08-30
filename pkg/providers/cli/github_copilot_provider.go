package cliprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

const (
	githubCopilotClientName = "picoclaw"
	githubCopilotSetupURL   = "https://github.com/github/copilot-sdk/blob/main/docs/getting-started.md"
	githubCopilotTokenURL   = "https://api.github.com/copilot_internal/v2/token"
	githubCopilotAPIBaseURL = "https://api.githubcopilot.com"
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
	tokenSource func() (string, error)
	model       string

	client  copilotClient
	session copilotSession

	mu sync.Mutex
}

func NewGitHubCopilotProvider(uri string, connectMode string, model string) (*GitHubCopilotProvider, error) {
	model, err := requireGitHubCopilotModel(model)
	if err != nil {
		return nil, err
	}
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
	model, err := requireGitHubCopilotModel(model)
	if err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if err := auth.ValidateGitHubCopilotToken(token); err != nil {
		return nil, err
	}
	return &GitHubCopilotProvider{
		connectMode: "token",
		token:       token,
		model:       model,
	}, nil
}

// SetTokenSource configures request-time GitHub token resolution. A nil source
// restores the fixed token supplied to NewGitHubCopilotProviderWithToken.
func (p *GitHubCopilotProvider) SetTokenSource(source func() (string, error)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.tokenSource = source
	p.mu.Unlock()
}

func (p *GitHubCopilotProvider) tokenForRequest() (string, error) {
	p.mu.Lock()
	source := p.tokenSource
	fixedToken := p.token
	p.mu.Unlock()

	token := fixedToken
	if source != nil {
		var err error
		token, err = source()
		if err != nil {
			return "", fmt.Errorf("resolving GitHub Copilot token: %w", err)
		}
	}
	token = strings.TrimSpace(token)
	if err := auth.ValidateGitHubCopilotToken(token); err != nil {
		return "", fmt.Errorf("invalid GitHub Copilot token: %w", err)
	}
	return token, nil
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
	model, err := requireGitHubCopilotModel(model)
	if err != nil {
		return nil, err
	}
	client := newCopilotClient(clientOptions)
	if startErr := client.Start(context.Background()); startErr != nil {
		return nil, fmt.Errorf(
			"can't connect to GitHub Copilot: %w; see %s for setup details",
			startErr,
			githubCopilotSetupURL,
		)
	}

	session, err := client.CreateSession(context.Background(), &copilot.SessionConfig{
		ClientName:          githubCopilotClientName,
		Model:               model,
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
		model:       model,
		client:      client,
		session:     session,
	}, nil
}

func requireGitHubCopilotModel(model string) (string, error) {
	model, err := protocoltypes.RequireModel(model)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(model, "auto") {
		return "", fmt.Errorf("github copilot model must be explicitly configured")
	}
	return model, nil
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
	model, err := requireGitHubCopilotModel(model)
	if err != nil {
		return nil, err
	}
	if p.connectMode == "token" {
		return p.chatWithToken(ctx, messages, model)
	}
	if configuredModel := strings.TrimSpace(p.model); configuredModel != "" && configuredModel != model {
		return nil, fmt.Errorf(
			"github copilot session model is %q, cannot use requested model %q",
			configuredModel,
			model,
		)
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
		Usage: githubCopilotUsageInfo(
			string(fullcontent),
			content,
			githubCopilotFloat(resp.Data.InputTokens),
			githubCopilotFloat(resp.Data.OutputTokens),
			githubCopilotFloat(resp.Data.CacheReadTokens),
			0,
		),
	}, nil
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
	token, err := p.tokenForRequest()
	if err != nil {
		return nil, err
	}
	authInfo, err := resolveGitHubCopilotAPIAuth(ctx, token)
	if err != nil {
		return nil, err
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
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
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
	return &LLMResponse{
		FinishReason: finishReason,
		Content:      content,
		Usage: githubCopilotUsageInfo(
			string(payload),
			content,
			chatResp.Usage.PromptTokens,
			chatResp.Usage.CompletionTokens,
			chatResp.Usage.PromptDetails.CachedTokens,
			chatResp.Usage.TotalTokens,
		),
	}, nil
}

func githubCopilotFloat(value *float64) int {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value <= 0 {
		return 0
	}
	const maximum = 2_147_483_647
	if *value >= maximum {
		return maximum
	}
	return int(math.Ceil(*value))
}

func githubCopilotUsageInfo(
	prompt string,
	content string,
	promptTokens int,
	completionTokens int,
	cachedTokens int,
	totalTokens int,
) *UsageInfo {
	estimated := promptTokens <= 0 || completionTokens <= 0 || cachedTokens < 0 ||
		cachedTokens > promptTokens || totalTokens < 0 ||
		totalTokens > 0 && totalTokens != promptTokens+completionTokens
	if promptTokens <= 0 {
		promptTokens = githubCopilotEstimatedTokens(prompt)
	}
	if completionTokens <= 0 {
		completionTokens = githubCopilotEstimatedTokens(content)
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	cachedTokens = min(cachedTokens, promptTokens)
	// Normalized usage always treats cached input and reasoning as subsets and
	// total as prompt plus completion. A transport total cannot be added again.
	totalTokens = promptTokens + completionTokens
	return &UsageInfo{
		PromptTokens: promptTokens, CompletionTokens: completionTokens,
		TotalTokens: totalTokens, CachedTokens: cachedTokens, Estimated: estimated,
	}
}

func githubCopilotEstimatedTokens(value string) int {
	if value == "" {
		return 0
	}
	// Three UTF-8 bytes per token is deliberately conservative when Copilot's
	// transport omits usage metadata.
	return (len([]byte(value)) + 2) / 3
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
