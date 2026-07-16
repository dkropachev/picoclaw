package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	agenttools "github.com/sipeed/picoclaw/pkg/tools"
)

func TestRegisterSharedTools_ThreadsDefaultAgentOnlyByDefault(t *testing.T) {
	al, cleanup := newThreadScopeAgentLoop(t, nil, &threadToolCaptureProvider{})
	defer cleanup()
	defer al.Close()

	mainAgent, ok := al.registry.GetAgent("main")
	if !ok {
		t.Fatal("main agent not found")
	}
	workerAgent, ok := al.registry.GetAgent("worker")
	if !ok {
		t.Fatal("worker agent not found")
	}

	if _, ok := mainAgent.Tools.Get(agenttools.ThreadsToolName); !ok {
		t.Fatal("default agent should have threads tool")
	}
	if _, ok := workerAgent.Tools.Get(agenttools.ThreadsToolName); ok {
		t.Fatal("non-default agent should not get threads tool by default")
	}

	workerMessages := workerAgent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "please start a coding thread",
	})
	if strings.Contains(workerMessages[0].Content, "## Thread Routing Policy") {
		t.Fatal("non-default agent without threads tool should not receive thread policy prompt")
	}
}

func TestRegisterSharedTools_ThreadsNonDefaultAgentExplicitOptIn(t *testing.T) {
	al, cleanup := newThreadScopeAgentLoop(t, map[string]string{
		"worker/AGENT.md": `---
tools: [threads]
---
# Worker
`,
	}, &threadToolCaptureProvider{})
	defer cleanup()
	defer al.Close()

	workerAgent, ok := al.registry.GetAgent("worker")
	if !ok {
		t.Fatal("worker agent not found")
	}
	if _, ok := workerAgent.Tools.Get(agenttools.ThreadsToolName); !ok {
		t.Fatal("non-default agent should get threads tool when AGENT.md explicitly opts in")
	}

	workerMessages := workerAgent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "please start a coding thread",
	})
	if !strings.Contains(workerMessages[0].Content, "## Thread Routing Policy") {
		t.Fatal("non-default agent with explicit threads tool should receive thread policy prompt")
	}
}

func TestSpawnSubTurn_DoesNotInheritThreadsTool(t *testing.T) {
	provider := &threadToolCaptureProvider{}
	al, cleanup := newThreadScopeAgentLoop(t, nil, provider)
	defer cleanup()
	defer al.Close()

	mainAgent, ok := al.registry.GetAgent("main")
	if !ok {
		t.Fatal("main agent not found")
	}
	if _, ok := mainAgent.Tools.Get(agenttools.ThreadsToolName); !ok {
		t.Fatal("test setup expected default agent to have threads tool")
	}

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-main",
		depth:          0,
		childTurnIDs:   []string{},
		pendingResults: make(chan *agenttools.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          mainAgent,
	}

	if _, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		Model:        "test-model",
		SystemPrompt: "inspect this task",
	}); err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}

	if slices.Contains(provider.lastToolNames(), agenttools.ThreadsToolName) {
		t.Fatalf("subturn inherited %q tool; tools = %v", agenttools.ThreadsToolName, provider.lastToolNames())
	}
	if strings.Contains(provider.lastSystemPrompt(), "## Thread Routing Policy") {
		t.Fatal("subturn prompt should not include thread policy after threads tool is removed")
	}
}

func newThreadScopeAgentLoop(
	t *testing.T,
	files map[string]string,
	provider providers.LLMProvider,
) (*AgentLoop, func()) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"main", "worker"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         root,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{
					ID:        "main",
					Default:   true,
					Workspace: filepath.Join(root, "main"),
				},
				{
					ID:        "worker",
					Workspace: filepath.Join(root, "worker"),
				},
			},
		},
		Tools: config.ToolsConfig{
			Threads: config.ThreadsToolConfig{
				Enabled: true,
				Policy: config.ThreadPolicyConfig{
					Enabled: true,
					Mode:    config.ThreadPolicyModeTool,
					Rules: []config.ThreadPolicyRule{
						{
							Type:        "coding",
							Description: "Use a coding thread for implementation work.",
						},
					},
				},
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)
	return al, func() { os.RemoveAll(root) }
}

type threadToolCaptureProvider struct {
	mu        sync.Mutex
	toolNames []string
	system    string
}

func (p *threadToolCaptureProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Function.Name)
	}
	system := ""
	if len(messages) > 0 {
		system = messages[0].Content
	}
	p.mu.Lock()
	p.toolNames = names
	p.system = system
	p.mu.Unlock()
	return &providers.LLMResponse{Content: "done"}, nil
}

func (p *threadToolCaptureProvider) GetDefaultModel() string {
	return "test-model"
}

func (p *threadToolCaptureProvider) lastToolNames() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.toolNames...)
}

func (p *threadToolCaptureProvider) lastSystemPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.system
}
