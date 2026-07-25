package cliprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/sipeed/picoclaw/pkg/auth"
)

const (
	githubCopilotDefaultModel = "auto"
	githubCopilotClientName   = "picoclaw"
)

type copilotClient interface {
	Start(context.Context) error
	Stop()
	CreateSession(context.Context, *copilot.SessionConfig) (copilotSession, error)
}

type copilotSession interface {
	SendAndWait(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error)
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

var newCopilotClient = newSDKCopilotClient

type GitHubCopilotProvider struct {
	uri         string
	connectMode string // "stdio" or "grpc"

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
	return newGitHubCopilotProvider("", "token", model, &copilot.ClientOptions{
		GitHubToken:     token,
		UseLoggedInUser: copilot.Bool(false),
	})
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
			"can't connect to GitHub Copilot: %w; see https://github.com/github/copilot-sdk/blob/main/docs/getting-started.md for setup details",
			err,
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
