package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

const controllerLocalReviewValidResponse = `{
  "outcome": "changes_required",
  "summary": "One race remains.",
  "findings": [{
    "severity": "high",
    "title": "Unprotected write",
    "file": "pkg/queue.go",
    "line": 42,
    "message": "The map write is outside the mutex.",
    "evidence": "Update mutates jobs after Unlock.",
    "impact": "Concurrent calls can panic.",
    "recommendation": "Move the write under the existing lock.",
    "validation": "Run the targeted race test."
  }]
}`

type controllerLocalReviewProviderCall struct {
	messages []providers.Message
	tools    int
	model    string
	options  map[string]any
}

type controllerLocalReviewProviderState struct {
	mu sync.Mutex

	response    string
	reasoning   string
	providerErr error
	factoryErr  error
	toolCall    bool
	created     []int
	closed      []int
	calls       []controllerLocalReviewProviderCall
}

func (state *controllerLocalReviewProviderState) factory(
	modelConfig *config.ModelConfig,
) (providers.LLMProvider, string, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.factoryErr != nil {
		return nil, "", state.factoryErr
	}
	id := len(state.created) + 1
	state.created = append(state.created, id)
	model := "controller-local-review-test"
	if modelConfig != nil && strings.TrimSpace(modelConfig.Model) != "" {
		model = modelConfig.Model
	}
	return &controllerLocalReviewProvider{id: id, state: state}, model, nil
}

func (state *controllerLocalReviewProviderState) snapshot() (
	[]int,
	[]int,
	[]controllerLocalReviewProviderCall,
) {
	state.mu.Lock()
	defer state.mu.Unlock()
	calls := make([]controllerLocalReviewProviderCall, len(state.calls))
	for index := range state.calls {
		calls[index] = state.calls[index]
		calls[index].messages = session.CloneMessages(state.calls[index].messages)
		calls[index].options = cloneAnyMap(state.calls[index].options)
	}
	return append([]int(nil), state.created...), append([]int(nil), state.closed...), calls
}

type controllerLocalReviewProvider struct {
	id    int
	state *controllerLocalReviewProviderState
}

func (provider *controllerLocalReviewProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	provider.state.mu.Lock()
	provider.state.calls = append(provider.state.calls, controllerLocalReviewProviderCall{
		messages: session.CloneMessages(messages),
		tools:    len(tools),
		model:    model,
		options:  cloneAnyMap(options),
	})
	response := provider.state.response
	reasoning := provider.state.reasoning
	providerErr := provider.state.providerErr
	toolCall := provider.state.toolCall
	provider.state.mu.Unlock()
	if providerErr != nil {
		return nil, providerErr
	}
	result := &providers.LLMResponse{Content: response, Reasoning: reasoning}
	if toolCall {
		result.ToolCalls = []providers.ToolCall{{
			ID:   "forbidden-private-review-tool-call",
			Type: "function",
			Function: &providers.FunctionCall{
				Name:      "workflow",
				Arguments: `{}`,
			},
		}}
	}
	return result, nil
}

func (provider *controllerLocalReviewProvider) Close() {
	provider.state.mu.Lock()
	provider.state.closed = append(provider.state.closed, provider.id)
	provider.state.mu.Unlock()
}

func TestControllerLocalReviewRunnerUsesFreshStrictIsolatedRequest(t *testing.T) {
	bootstrap := &workflowReadOnlyCaptureProvider{}
	loop, reviewAgent, _, _ := newWorkflowReadOnlyTestLoop(t, bootstrap)
	state := &controllerLocalReviewProviderState{response: controllerLocalReviewValidResponse}
	loop.providerFactory = state.factory

	contextTracker := &trackingContextManager{}
	originalContextManager := loop.contextManager
	loop.contextManager = contextTracker
	t.Cleanup(func() { loop.contextManager = originalContextManager })

	canary := "CONTROLLER-LOCAL-REVIEW-WORKSPACE-CANARY"
	if err := os.WriteFile(filepath.Join(reviewAgent.Workspace, "AGENTS.md"), []byte(canary), 0o600); err != nil {
		t.Fatalf("write workspace canary: %v", err)
	}
	sessionsDir := filepath.Join(reviewAgent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), reviewAgent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)
	messageBus, ok := loop.bus.(*bus.MessageBus)
	if !ok {
		t.Fatalf("message bus = %T, want *bus.MessageBus", loop.bus)
	}
	beforeBus := messageBus.Stats()

	if !loop.ControllerLocalReviewReady("main") {
		t.Fatal("ControllerLocalReviewReady(main) = false")
	}
	runner, err := loop.NewControllerLocalReviewRunner("main")
	if err != nil {
		t.Fatalf("NewControllerLocalReviewRunner() error = %v", err)
	}
	contextText := `{"goal":"remove the queue race","candidate":{"review_digest":"opaque"}}`
	result, err := runner.Run(context.Background(), ControllerLocalReviewRequest{Context: contextText})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantLine := 42
	want := ControllerLocalReviewResult{
		Outcome: ControllerLocalReviewChangesRequired,
		Summary: "One race remains.",
		Findings: []ControllerLocalReviewFinding{{
			Severity:       ControllerLocalReviewSeverityHigh,
			Title:          "Unprotected write",
			File:           "pkg/queue.go",
			Line:           &wantLine,
			Message:        "The map write is outside the mutex.",
			Evidence:       "Update mutates jobs after Unlock.",
			Impact:         "Concurrent calls can panic.",
			Recommendation: "Move the write under the existing lock.",
			Validation:     "Run the targeted race test.",
		}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("Run() result = %#v, want %#v", result, want)
	}

	created, closed, calls := state.snapshot()
	if !reflect.DeepEqual(created, []int{1}) || !reflect.DeepEqual(closed, []int{1}) {
		t.Fatalf("isolated providers created/closed = %v/%v, want [1]/[1]", created, closed)
	}
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.tools != 0 {
		t.Fatalf("provider tools = %d, want zero", call.tools)
	}
	if cacheKey, present := call.options["prompt_cache_key"]; present {
		t.Fatalf("provider prompt cache key = %#v, want absent", cacheKey)
	}
	if workflowMessagesHavePromptCacheControl(call.messages) {
		t.Fatalf("provider messages retained cache controls: %#v", call.messages)
	}
	if len(call.messages) != 2 || call.messages[0].Role != "system" ||
		call.messages[0].Content != controllerLocalReviewSystemPrompt() ||
		call.messages[1].Role != "user" ||
		call.messages[1].Content != controllerLocalReviewUserMessage(contextText) {
		t.Fatalf("isolated provider messages = %#v", call.messages)
	}
	for _, forbidden := range []string{
		canary,
		"existing problem context",
		"existing decision summary",
		"You are picoclaw",
		"## Current Time",
		"## Runtime",
		reviewAgent.Workspace,
	} {
		if workflowMessagesContain(call.messages, forbidden) {
			t.Fatalf("provider request leaked %q: %#v", forbidden, call.messages)
		}
	}
	workflowAssertSessionStoreUnchanged(t, reviewAgent, sessionsDir, beforeCatalog, beforeFiles)
	if contextTracker.assembleCalls.Load() != 0 || contextTracker.compactCalls.Load() != 0 ||
		contextTracker.ingestCalls.Load() != 0 || contextTracker.clearCalls.Load() != 0 {
		t.Fatalf(
			"context manager calls = assemble:%d compact:%d ingest:%d clear:%d, want zero",
			contextTracker.assembleCalls.Load(),
			contextTracker.compactCalls.Load(),
			contextTracker.ingestCalls.Load(),
			contextTracker.clearCalls.Load(),
		)
	}
	afterBus := messageBus.Stats()
	if afterBus.Outbound != beforeBus.Outbound || afterBus.OutboundMedia != beforeBus.OutboundMedia {
		t.Fatalf("private review published to bus: before=%#v after=%#v", beforeBus, afterBus)
	}
}

func TestControllerLocalReviewRunnerCreatesFreshProviderForEveryRun(t *testing.T) {
	bootstrap := &workflowReadOnlyCaptureProvider{}
	loop := newControllerLocalReviewTestLoop(t, bootstrap)
	state := &controllerLocalReviewProviderState{
		response: `{"outcome":"passed","summary":"No issue found.","findings":[]}`,
	}
	loop.providerFactory = state.factory
	runner, err := loop.NewControllerLocalReviewRunner("main")
	if err != nil {
		t.Fatalf("NewControllerLocalReviewRunner() error = %v", err)
	}
	for index := 0; index < 2; index++ {
		result, runErr := runner.Run(
			context.Background(),
			ControllerLocalReviewRequest{Context: fmt.Sprintf(`{"attempt":%d}`, index)},
		)
		if runErr != nil || result.Outcome != ControllerLocalReviewPassed {
			t.Fatalf("Run(%d) = (%#v, %v)", index, result, runErr)
		}
	}
	created, closed, calls := state.snapshot()
	if !reflect.DeepEqual(created, []int{1, 2}) ||
		!reflect.DeepEqual(closed, []int{1, 2}) || len(calls) != 2 {
		t.Fatalf("fresh provider lifecycle = created:%v closed:%v calls:%d", created, closed, len(calls))
	}
}

func TestControllerLocalReviewFactoryRequiresExactCurrentAgent(t *testing.T) {
	bootstrap := &workflowReadOnlyCaptureProvider{}
	loop, reviewAgent, _, _ := newWorkflowReadOnlyTestLoop(t, bootstrap)
	state := &controllerLocalReviewProviderState{response: controllerLocalReviewValidResponse}
	loop.providerFactory = state.factory

	for _, agentID := range []string{"", " main", "main ", "Main", "main/child"} {
		if loop.ControllerLocalReviewReady(agentID) {
			t.Fatalf("ControllerLocalReviewReady(%q) = true", agentID)
		}
		if _, err := loop.NewControllerLocalReviewRunner(agentID); !errors.Is(
			err,
			ErrControllerLocalReviewUnavailable,
		) {
			t.Fatalf("NewControllerLocalReviewRunner(%q) error = %v", agentID, err)
		}
	}
	if _, err := loop.NewControllerLocalReviewRunner("missing"); !errors.Is(
		err,
		ErrControllerLocalReviewUnavailable,
	) {
		t.Fatalf("missing agent error = %v", err)
	}
	var nilLoop *AgentLoop
	if _, err := nilLoop.NewControllerLocalReviewRunner("main"); !errors.Is(
		err,
		ErrControllerLocalReviewUnavailable,
	) {
		t.Fatalf("nil loop error = %v", err)
	}

	runner, err := loop.NewControllerLocalReviewRunner("main")
	if err != nil {
		t.Fatalf("NewControllerLocalReviewRunner(main) error = %v", err)
	}
	loop.registry.mu.Lock()
	loop.registry.agents["main"] = &AgentInstance{ID: "main"}
	loop.registry.mu.Unlock()
	if _, err = runner.Run(
		context.Background(),
		ControllerLocalReviewRequest{Context: `{"review":"bounded"}`},
	); !errors.Is(err, ErrControllerLocalReviewUnavailable) {
		t.Fatalf("stale-generation runner error = %v", err)
	}
	if created, _, calls := state.snapshot(); len(created) != 0 || len(calls) != 0 {
		t.Fatalf("stale runner reached provider: created=%v calls=%d", created, len(calls))
	}
	loop.registry.mu.Lock()
	loop.registry.agents["main"] = reviewAgent
	loop.registry.mu.Unlock()
}

func TestControllerLocalReviewRunSanitizesFailuresAndRejectsToolCalls(t *testing.T) {
	bootstrap := &workflowReadOnlyCaptureProvider{}
	loop := newControllerLocalReviewTestLoop(t, bootstrap)
	state := &controllerLocalReviewProviderState{response: controllerLocalReviewValidResponse}
	loop.providerFactory = state.factory
	runner, err := loop.NewControllerLocalReviewRunner("main")
	if err != nil {
		t.Fatalf("NewControllerLocalReviewRunner() error = %v", err)
	}
	request := ControllerLocalReviewRequest{Context: `{"review":"bounded"}`}
	privateValues := []string{
		"secret-provider-account",
		"secret-model-response",
		"secret-tool-call",
	}

	state.providerErr = errors.New(privateValues[0])
	_, err = runner.Run(context.Background(), request)
	assertControllerLocalReviewSafeFailure(t, err, privateValues)
	state.providerErr = nil
	state.response = "prose " + privateValues[1]
	_, err = runner.Run(context.Background(), request)
	assertControllerLocalReviewSafeFailure(t, err, privateValues)
	state.response = " "
	state.reasoning = controllerLocalReviewValidResponse
	_, err = runner.Run(context.Background(), request)
	assertControllerLocalReviewSafeFailure(t, err, privateValues)
	state.reasoning = ""
	state.response = controllerLocalReviewValidResponse
	state.toolCall = true
	_, err = runner.Run(context.Background(), request)
	assertControllerLocalReviewSafeFailure(t, err, privateValues)
	state.toolCall = false
	state.factoryErr = errors.New("factory " + privateValues[0])
	_, err = runner.Run(context.Background(), request)
	assertControllerLocalReviewSafeFailure(t, err, privateValues)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = runner.Run(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Run() error = %v, want context.Canceled", err)
	}

	created, _, calls := state.snapshot()
	if len(created) != 4 || len(calls) != 4 {
		t.Fatalf(
			"provider lifecycle = created:%d calls:%d, want four calls before factory failure",
			len(created),
			len(calls),
		)
	}
}

func assertControllerLocalReviewSafeFailure(
	t *testing.T,
	err error,
	privateValues []string,
) {
	t.Helper()
	if !errors.Is(err, ErrControllerLocalReviewFailed) ||
		err.Error() != ErrControllerLocalReviewFailed.Error() {
		t.Fatalf("Run() error = %v, want exact safe failure", err)
	}
	for _, privateValue := range privateValues {
		if strings.Contains(err.Error(), privateValue) {
			t.Fatalf("safe error %q exposed %q", err, privateValue)
		}
	}
}

func TestParseControllerLocalReviewResponseStrictContractAndBounds(t *testing.T) {
	validAttention := `{
  "outcome": "attention_required",
  "summary": "A product decision is required.",
  "findings": []
}`
	if result, err := parseControllerLocalReviewResponse(validAttention); err != nil ||
		result.Outcome != ControllerLocalReviewAttentionRequired || result.Findings == nil {
		t.Fatalf("valid pretty response = (%#v, %v)", result, err)
	}

	validFinding := `{"severity":"low","title":"T","message":"M"}`
	tooMany := make([]string, MaxControllerLocalReviewFindings+1)
	for index := range tooMany {
		tooMany[index] = validFinding
	}
	aggregate := make([]string, 9)
	for index := range aggregate {
		aggregate[index] = fmt.Sprintf(
			`{"severity":"low","title":"T","message":"M","evidence":%q}`,
			strings.Repeat("e", MaxControllerLocalReviewFindingEvidenceBytes),
		)
	}
	tests := map[string]string{
		"empty":           "",
		"prose":           "not json",
		"fenced":          "```json\n" + validAttention + "\n```",
		"prefixed":        "review result: " + validAttention,
		"trailing JSON":   validAttention + ` {}`,
		"unknown root":    `{"outcome":"passed","summary":"ok","findings":[],"publish":true}`,
		"missing summary": `{"outcome":"passed","findings":[]}`,
		"duplicate root": `{
  "outcome":"passed","outcome":"changes_required","summary":"ok","findings":[]
}`,
		"bad outcome": controllerLocalReviewResponseForTest(
			"merge", "ok", "",
		),
		"passed finding": controllerLocalReviewResponseForTest(
			"passed", "ok", validFinding,
		),
		"changes empty": controllerLocalReviewResponseForTest(
			"changes_required", "ok", "",
		),
		"unknown finding": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			`{"severity":"low","title":"T","message":"M","action":"push"}`,
		),
		"duplicate finding": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			`{"severity":"low","title":"T","title":"other","message":"M"}`,
		),
		"bad severity": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			`{"severity":"blocker","title":"T","message":"M"}`,
		),
		"missing title": controllerLocalReviewResponseForTest(
			"changes_required", "ok", `{"severity":"low","message":"M"}`,
		),
		"zero line": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			`{"severity":"low","title":"T","message":"M","line":0}`,
		),
		"fractional line": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			`{"severity":"low","title":"T","message":"M","line":1.5}`,
		),
		"large line": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			`{"severity":"low","title":"T","message":"M","line":2147483648}`,
		),
		"untrimmed required": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			`{"severity":"low","title":" T","message":"M"}`,
		),
		"untrimmed optional": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			`{"severity":"low","title":"T","message":"M","file":" f.go"}`,
		),
		"oversized summary": controllerLocalReviewResponseForTest(
			"passed", strings.Repeat("s", MaxControllerLocalReviewSummaryBytes+1), "",
		),
		"oversized title": controllerLocalReviewResponseForTest(
			"changes_required",
			"ok",
			fmt.Sprintf(
				`{"severity":"low","title":%q,"message":"M"}`,
				strings.Repeat("t", MaxControllerLocalReviewFindingTitleBytes+1),
			),
		),
		"too many findings": controllerLocalReviewResponseForTest(
			"changes_required", "ok", strings.Join(tooMany, ","),
		),
		"aggregate findings": controllerLocalReviewResponseForTest(
			"changes_required", "ok", strings.Join(aggregate, ","),
		),
		"oversized raw response": strings.Repeat(" ", MaxControllerLocalReviewResponseBytes+1),
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			if result, err := parseControllerLocalReviewResponse(response); !errors.Is(
				err,
				ErrControllerLocalReviewFailed,
			) || !reflect.DeepEqual(result, ControllerLocalReviewResult{}) {
				t.Fatalf("parse response = (%#v, %v), want safe failure", result, err)
			}
		})
	}
}

func TestControllerLocalReviewRequestValidationAndStructuralRedaction(t *testing.T) {
	bootstrap := &workflowReadOnlyCaptureProvider{}
	loop := newControllerLocalReviewTestLoop(t, bootstrap)
	state := &controllerLocalReviewProviderState{response: controllerLocalReviewValidResponse}
	loop.providerFactory = state.factory
	runner, err := loop.NewControllerLocalReviewRunner("main")
	if err != nil {
		t.Fatalf("NewControllerLocalReviewRunner() error = %v", err)
	}
	invalid := []string{
		"",
		" surrounded ",
		"nul\x00context",
		string([]byte{'{', 0xff, '}'}),
		strings.Repeat("x", MaxControllerLocalReviewContextBytes+1),
	}
	for _, contextText := range invalid {
		if _, runErr := runner.Run(
			context.Background(),
			ControllerLocalReviewRequest{Context: contextText},
		); !errors.Is(runErr, ErrControllerLocalReviewInvalid) {
			t.Fatalf("Run(%q) error = %v, want invalid", contextText[:min(len(contextText), 32)], runErr)
		}
	}
	if created, _, calls := state.snapshot(); len(created) != 0 || len(calls) != 0 {
		t.Fatalf("invalid request reached provider: created=%d calls=%d", len(created), len(calls))
	}

	line := 7
	for name, value := range map[string]any{
		"request": ControllerLocalReviewRequest{Context: "private-review-context"},
		"result": ControllerLocalReviewResult{
			Outcome: ControllerLocalReviewAttentionRequired,
			Summary: "private-review-summary",
			Findings: []ControllerLocalReviewFinding{{
				Severity: ControllerLocalReviewSeverityHigh,
				Title:    "private-review-finding",
				Line:     &line,
			}},
		},
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil || string(encoded) != "{}" {
			t.Fatalf("json.Marshal(%s) = (%s, %v), want {}", name, encoded, marshalErr)
		}
	}
}

func TestControllerLocalReviewSkipsHooksMCPAndWorkflowRuntime(t *testing.T) {
	bootstrap := &workflowReadOnlyCaptureProvider{}
	loop := newControllerLocalReviewTestLoop(t, bootstrap)
	state := &controllerLocalReviewProviderState{
		response: `{"outcome":"passed","summary":"No issue found.","findings":[]}`,
	}
	loop.providerFactory = state.factory
	spy := &workflowEphemeralHookSpy{}
	hooks := NewHookManager(nil)
	if err := hooks.Mount(HookRegistration{Name: "controller-review-spy", Hook: spy}); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}
	originalHooks := loop.hooks
	loop.hooks = hooks
	t.Cleanup(func() {
		loop.hooks = originalHooks
		hooks.Close()
	})
	marker := filepath.Join(t.TempDir(), "mcp-started")
	loop.cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"forbidden-review-server": {
				Enabled: true,
				Command: "sh",
				Args: []string{
					"-c",
					`printf started > "$1"`,
					"controller-local-review-test",
					marker,
				},
			},
		},
	}
	loop.cfg.Workflows.Enabled = true

	runner, err := loop.NewControllerLocalReviewRunner("main")
	if err != nil {
		t.Fatalf("NewControllerLocalReviewRunner() error = %v", err)
	}
	if _, err = runner.Run(
		context.Background(),
		ControllerLocalReviewRequest{Context: `{"review":"bounded"}`},
	); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if spy.beforeCalls.Load() != 0 || spy.afterCalls.Load() != 0 {
		t.Fatalf("hook calls = before:%d after:%d, want zero", spy.beforeCalls.Load(), spy.afterCalls.Load())
	}
	if loop.mcp.getManager() != nil || loop.mcp.getInitErr() != nil {
		t.Fatal("private review initialized MCP")
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("private review executed MCP command: %v", statErr)
	}
}

func TestControllerLocalReviewPromptDigestAndContractAreStable(t *testing.T) {
	digest := ControllerLocalReviewPromptDigest()
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) ||
		digest != ControllerLocalReviewPromptDigest() {
		t.Fatalf("ControllerLocalReviewPromptDigest() = %q", digest)
	}
	prompt := controllerLocalReviewSystemPrompt()
	for _, required := range []string{
		"untrusted data",
		"no tools",
		"Return only valid JSON",
		`"changes_required"`,
		`"attention_required"`,
		`"additionalProperties": false`,
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("isolated prompt omitted %q:\n%s", required, prompt)
		}
	}
}

func controllerLocalReviewResponseForTest(outcome, summary, findings string) string {
	return fmt.Sprintf(
		`{"outcome":%q,"summary":%q,"findings":[%s]}`,
		outcome,
		summary,
		findings,
	)
}

func newControllerLocalReviewTestLoop(
	t *testing.T,
	provider *workflowReadOnlyCaptureProvider,
) *AgentLoop {
	t.Helper()
	loop, reviewAgent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
	if reviewAgent == nil || canonicalKey == "" || alias == "" {
		t.Fatal("workflow read-only fixture is incomplete")
	}
	return loop
}
