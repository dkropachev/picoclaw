package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/session"
)

type localRepairTestAcquirer struct {
	mu        sync.Mutex
	workspace gitworkspace.WorkspaceInfo
	errors    map[int]error
	transform func(int, gitworkspace.WorkspaceInfo) gitworkspace.WorkspaceInfo
	calls     []gitworkspace.PinnedAcquireRequest
}

func (acquirer *localRepairTestAcquirer) AcquirePinned(
	_ context.Context,
	request gitworkspace.PinnedAcquireRequest,
) (gitworkspace.WorkspaceInfo, error) {
	acquirer.mu.Lock()
	acquirer.calls = append(acquirer.calls, request)
	call := len(acquirer.calls)
	workspace := cloneLocalRepairTestWorkspace(acquirer.workspace)
	err := acquirer.errors[call]
	transform := acquirer.transform
	acquirer.mu.Unlock()

	if transform != nil {
		workspace = transform(call, workspace)
	}
	if err != nil {
		return gitworkspace.WorkspaceInfo{}, err
	}
	return workspace, nil
}

func (acquirer *localRepairTestAcquirer) Calls() []gitworkspace.PinnedAcquireRequest {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	return append([]gitworkspace.PinnedAcquireRequest(nil), acquirer.calls...)
}

func cloneLocalRepairTestWorkspace(
	workspace gitworkspace.WorkspaceInfo,
) gitworkspace.WorkspaceInfo {
	cloned := workspace
	if workspace.LockedBy != nil {
		lock := *workspace.LockedBy
		cloned.LockedBy = &lock
	}
	if workspace.DroppedAt != nil {
		droppedAt := *workspace.DroppedAt
		cloned.DroppedAt = &droppedAt
	}
	return cloned
}

type localRepairTestProviderCall struct {
	messages    []providers.Message
	definitions []providers.ToolDefinition
	model       string
	options     map[string]any
}

type localRepairTestProvider struct {
	mu      sync.Mutex
	calls   []localRepairTestProviderCall
	handler func(
		int,
		[]providers.Message,
		[]providers.ToolDefinition,
		string,
		map[string]any,
	) (*providers.LLMResponse, error)
}

func (provider *localRepairTestProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	clonedOptions := make(map[string]any, len(options))
	for key, value := range options {
		clonedOptions[key] = value
	}
	call := localRepairTestProviderCall{
		messages:    session.CloneMessages(messages),
		definitions: append([]providers.ToolDefinition(nil), definitions...),
		model:       model,
		options:     clonedOptions,
	}

	provider.mu.Lock()
	provider.calls = append(provider.calls, call)
	index := len(provider.calls) - 1
	handler := provider.handler
	provider.mu.Unlock()
	if handler == nil {
		return &providers.LLMResponse{Content: "done"}, nil
	}
	return handler(index, messages, definitions, model, options)
}

func (provider *localRepairTestProvider) Calls() []localRepairTestProviderCall {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	calls := make([]localRepairTestProviderCall, len(provider.calls))
	copy(calls, provider.calls)
	return calls
}

func newLocalRepairTestWorkspace(
	t *testing.T,
) (gitworkspace.PinnedAcquireRequest, gitworkspace.WorkspaceInfo, string) {
	t.Helper()
	root := filepath.Clean(t.TempDir())
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create Git control directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("safe-config\n"), 0o644); err != nil {
		t.Fatalf("write Git config fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write repository fixture: %v", err)
	}

	pin := gitworkspace.PinnedAcquireRequest{
		Repository:     "git@github.com:acme/repository.git",
		SourceRef:      "repair-head",
		ExpectedCommit: strings.Repeat("a", 40),
		ReservationKey: "repair-test-reservation",
		AgentID:        "main",
	}
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	workspace := gitworkspace.WorkspaceInfo{
		ID:        "workspace-one",
		RepoID:    "repository-one",
		RemoteURL: pin.Repository,
		Ref:       pin.SourceRef,
		Path:      root,
		CreatedAt: now,
		UpdatedAt: now,
		LockedBy: &gitworkspace.LockInfo{
			SessionKey:  pin.ReservationKey,
			AgentID:     pin.AgentID,
			LockedAt:    now,
			HeartbeatAt: now,
		},
		Status: "locked",
	}
	return pin, workspace, root
}

func newLocalRepairTestRunner(
	t *testing.T,
	acquirer *localRepairTestAcquirer,
	provider *localRepairTestProvider,
	maxIterations int,
) *LocalRepairRunner {
	t.Helper()
	runner, err := NewLocalRepairRunner(LocalRepairRunnerConfig{
		Workspaces:    acquirer,
		Provider:      provider,
		Model:         "repair-model",
		MaxIterations: maxIterations,
		MaxTokens:     1234,
		Temperature:   0.25,
	})
	if err != nil {
		t.Fatalf("NewLocalRepairRunner() error = %v", err)
	}
	return runner
}

func localRepairTestToolCall(
	id string,
	name string,
	arguments map[string]any,
) providers.ToolCall {
	return providers.ToolCall{
		ID:        id,
		Name:      name,
		Arguments: arguments,
		Function:  &providers.FunctionCall{Name: name},
	}
}

func localRepairTestRunRequest(pin gitworkspace.PinnedAcquireRequest) LocalRepairRequest {
	return LocalRepairRequest{
		Pin:         pin,
		Instruction: "Make the requested focused repair.",
		Context:     "A reviewer found one behavior that needs correction.",
	}
}

func TestLocalRepairRunnerExactCapabilitiesPinAndEdit(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{}
	provider.handler = func(
		index int,
		_ []providers.Message,
		_ []providers.ToolDefinition,
		_ string,
		_ map[string]any,
	) (*providers.LLMResponse, error) {
		switch index {
		case 0:
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				localRepairTestToolCall("edit-1", "edit_file", map[string]any{
					"path":     "README.md",
					"old_text": "before\n",
					"new_text": "after\n",
				}),
			}}, nil
		case 1:
			return &providers.LLMResponse{Content: "changed README"}, nil
		default:
			t.Fatalf("unexpected provider call %d", index+1)
			return nil, nil
		}
	}

	result, err := newLocalRepairTestRunner(t, acquirer, provider, 4).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Content != "changed README" || result.Iterations != 2 || result.WorkspaceID != workspace.ID {
		t.Fatalf("Run() result = %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(content) != "after\n" {
		t.Fatalf("README.md = %q, want %q", content, "after\n")
	}

	workspaceCalls := acquirer.Calls()
	if len(workspaceCalls) != 2 {
		t.Fatalf("AcquirePinned() calls = %d, want preflight and postflight", len(workspaceCalls))
	}
	for index, call := range workspaceCalls {
		if call != pin {
			t.Fatalf("AcquirePinned() call %d = %#v, want exact pin %#v", index+1, call, pin)
		}
	}

	providerCalls := provider.Calls()
	if len(providerCalls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(providerCalls))
	}
	expectedNames := []string{"apply_patch", "edit_file", "list_dir", "read_file"}
	for index, call := range providerCalls {
		if call.model != "repair-model" {
			t.Errorf("provider call %d model = %q", index+1, call.model)
		}
		if len(call.options) != 2 || call.options["max_tokens"] != 1234 || call.options["temperature"] != 0.25 {
			t.Errorf("provider call %d options = %#v, want only fixed repair options", index+1, call.options)
		}
		var names []string
		for _, definition := range call.definitions {
			names = append(names, definition.Function.Name)
		}
		slices.Sort(names)
		if !slices.Equal(names, expectedNames) {
			t.Errorf("provider call %d tools = %v, want %v", index+1, names, expectedNames)
		}
	}
	if len(providerCalls[0].messages) != 2 ||
		providerCalls[0].messages[0].Role != "system" ||
		providerCalls[0].messages[1].Role != "user" {
		t.Fatalf("initial provider messages = %#v", providerCalls[0].messages)
	}
	for _, message := range providerCalls[0].messages {
		if strings.Contains(message.Content, root) {
			t.Fatalf("initial prompt exposed checkout path %q", root)
		}
	}
	foundToolResult := false
	for _, message := range providerCalls[1].messages {
		if message.Role == "tool" && message.ToolCallID == "edit-1" {
			foundToolResult = true
		}
	}
	if !foundToolResult {
		t.Fatal("second provider call did not receive the edit result")
	}
}

func TestLocalRepairRunnerSerializesReservationAcrossRepositoryAliases(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	entered := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	providerHandler := func(
		_ int,
		_ []providers.Message,
		_ []providers.ToolDefinition,
		_ string,
		_ map[string]any,
	) (*providers.LLMResponse, error) {
		entered <- struct{}{}
		<-release
		return &providers.LLMResponse{Content: "done"}, nil
	}
	firstProvider := &localRepairTestProvider{handler: providerHandler}
	secondProvider := &localRepairTestProvider{handler: providerHandler}
	firstRunner := newLocalRepairTestRunner(t, acquirer, firstProvider, 2)
	secondRunner := newLocalRepairTestRunner(t, acquirer, secondProvider, 2)
	secondPin := pin
	secondPin.Repository = "https://github.com/acme/repository.git"

	type runOutcome struct {
		result LocalRepairResult
		err    error
	}
	outcomes := make(chan runOutcome, 2)
	go func() {
		result, err := firstRunner.Run(t.Context(), localRepairTestRunRequest(pin))
		outcomes <- runOutcome{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first provider did not start")
	}
	go func() {
		result, err := secondRunner.Run(t.Context(), localRepairTestRunRequest(secondPin))
		outcomes <- runOutcome{result: result, err: err}
	}()

	select {
	case <-entered:
		release <- struct{}{}
		release <- struct{}{}
		for range 2 {
			<-outcomes
		}
		t.Fatal("repository aliases entered one reservation concurrently")
	case <-time.After(75 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("second provider did not start after first reservation released")
	}
	release <- struct{}{}
	for range 2 {
		outcome := <-outcomes
		if outcome.err != nil || outcome.result.Content != "done" {
			t.Fatalf("serialized Run() outcome = %#v, error = %v", outcome.result, outcome.err)
		}
	}
	if len(acquirer.Calls()) != 4 {
		t.Fatalf("AcquirePinned() calls = %d, want two preflights and postflights", len(acquirer.Calls()))
	}
}

func TestLocalRepairRunnerPreflightFailureDoesNotCallProvider(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{
		workspace: workspace,
		errors:    map[int]error{1: errors.New("pin rejected")},
	}
	provider := &localRepairTestProvider{
		handler: func(
			_ int,
			_ []providers.Message,
			_ []providers.ToolDefinition,
			_ string,
			_ map[string]any,
		) (*providers.LLMResponse, error) {
			t.Fatal("provider called after failed pin preflight")
			return nil, nil
		},
	}

	_, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if !errors.Is(err, ErrLocalRepairPin) {
		t.Fatalf("Run() error = %v, want ErrLocalRepairPin", err)
	}
	if len(acquirer.Calls()) != 1 {
		t.Fatalf("AcquirePinned() calls = %d, want preflight only", len(acquirer.Calls()))
	}
	if len(provider.Calls()) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(provider.Calls()))
	}
	content, readErr := os.ReadFile(filepath.Join(root, "README.md"))
	if readErr != nil || string(content) != "before\n" {
		t.Fatalf("README.md changed after failed preflight: content=%q error=%v", content, readErr)
	}
}

func TestLocalRepairRunnerPostflightsAfterCheckoutValidationFailure(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("remove Git control fixture: %v", err)
	}
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{
		handler: func(
			_ int,
			_ []providers.Message,
			_ []providers.ToolDefinition,
			_ string,
			_ map[string]any,
		) (*providers.LLMResponse, error) {
			t.Fatal("provider called after checkout validation failure")
			return nil, nil
		},
	}

	_, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if !errors.Is(err, ErrLocalRepairPin) {
		t.Fatalf("Run() error = %v, want ErrLocalRepairPin", err)
	}
	if len(acquirer.Calls()) != 2 {
		t.Fatalf("AcquirePinned() calls = %d, want preflight and postflight", len(acquirer.Calls()))
	}
	if len(provider.Calls()) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(provider.Calls()))
	}
}

func TestLocalRepairRunnerCancellationAfterProviderPreventsToolExecution(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	ctx, cancel := context.WithCancel(context.Background())
	provider := &localRepairTestProvider{
		handler: func(
			_ int,
			_ []providers.Message,
			_ []providers.ToolDefinition,
			_ string,
			_ map[string]any,
		) (*providers.LLMResponse, error) {
			cancel()
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				localRepairTestToolCall("late-edit", "edit_file", map[string]any{
					"path":     "README.md",
					"old_text": "before\n",
					"new_text": "after\n",
				}),
			}}, nil
		},
	}

	_, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
		ctx,
		localRepairTestRunRequest(pin),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "README.md"))
	if readErr != nil || string(content) != "before\n" {
		t.Fatalf("README.md changed after cancellation: content=%q error=%v", content, readErr)
	}
	if len(acquirer.Calls()) != 2 {
		t.Fatalf("AcquirePinned() calls = %d, want preflight and postflight", len(acquirer.Calls()))
	}
}

func TestLocalRepairRunnerSanitizesCheckoutPathFromToolErrors(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{}
	provider.handler = func(
		index int,
		messages []providers.Message,
		_ []providers.ToolDefinition,
		_ string,
		_ map[string]any,
	) (*providers.LLMResponse, error) {
		if index == 0 {
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				localRepairTestToolCall("missing", "apply_patch", map[string]any{
					"patch": "*** Begin Patch\n" +
						"*** Update File: missing.txt\n" +
						"@@\n-before\n+after\n*** End Patch",
				}),
			}}, nil
		}
		for _, message := range messages {
			if strings.Contains(message.Content, root) {
				t.Fatalf("tool result exposed checkout path %q: %#v", root, messages)
			}
		}
		return &providers.LLMResponse{Content: "handled missing file at " + root}, nil
	}

	result, err := newLocalRepairTestRunner(t, acquirer, provider, 3).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Content != "handled missing file at [checkout]" ||
		strings.Contains(result.Content, root) {
		t.Fatalf("Run() result = %#v", result)
	}
}

func TestLocalRepairRunnerDeniesGitControlPaths(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*testing.T, string)
		toolCall  func() providers.ToolCall
		assertion func(*testing.T, string)
	}{
		{
			name: "direct",
			toolCall: func() providers.ToolCall {
				return localRepairTestToolCall("direct", "edit_file", map[string]any{
					"path": ".git/config", "old_text": "safe-config\n", "new_text": "evil\n",
				})
			},
		},
		{
			name: "case alias",
			toolCall: func() providers.ToolCall {
				return localRepairTestToolCall("case", "edit_file", map[string]any{
					"path": ".GIT/config", "old_text": "safe-config\n", "new_text": "evil\n",
				})
			},
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Symlink(filepath.Join(".git", "config"), filepath.Join(root, "control-link")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			toolCall: func() providers.ToolCall {
				return localRepairTestToolCall("symlink", "edit_file", map[string]any{
					"path": "control-link", "old_text": "safe-config\n", "new_text": "evil\n",
				})
			},
		},
		{
			name: "late forbidden patch",
			toolCall: func() providers.ToolCall {
				return localRepairTestToolCall("patch", "apply_patch", map[string]any{
					"patch": "*** Begin Patch\n" +
						"*** Add File: allowed.txt\n" +
						"+created\n" +
						"*** Update File: .git/config\n" +
						"@@\n" +
						"-safe-config\n" +
						"+evil\n" +
						"*** End Patch",
				})
			},
			assertion: func(t *testing.T, root string) {
				t.Helper()
				_, err := os.Stat(filepath.Join(root, "allowed.txt"))
				if !os.IsNotExist(err) {
					t.Fatalf("allowed.txt exists after a later patch path was denied: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pin, workspace, root := newLocalRepairTestWorkspace(t)
			if test.prepare != nil {
				test.prepare(t, root)
			}
			acquirer := &localRepairTestAcquirer{workspace: workspace}
			provider := &localRepairTestProvider{}
			provider.handler = func(
				index int,
				_ []providers.Message,
				_ []providers.ToolDefinition,
				_ string,
				_ map[string]any,
			) (*providers.LLMResponse, error) {
				if index == 0 {
					return &providers.LLMResponse{ToolCalls: []providers.ToolCall{test.toolCall()}}, nil
				}
				return &providers.LLMResponse{Content: "handled denial"}, nil
			}

			result, err := newLocalRepairTestRunner(t, acquirer, provider, 3).Run(
				t.Context(),
				localRepairTestRunRequest(pin),
			)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if result.Content != "handled denial" {
				t.Fatalf("Run() content = %q", result.Content)
			}
			gitConfig, readErr := os.ReadFile(filepath.Join(root, ".git", "config"))
			if readErr != nil || string(gitConfig) != "safe-config\n" {
				t.Fatalf("Git config mutated: content=%q error=%v", gitConfig, readErr)
			}
			if test.assertion != nil {
				test.assertion(t, root)
			}
			calls := provider.Calls()
			if len(calls) != 2 {
				t.Fatalf("provider calls = %d, want 2", len(calls))
			}
			foundDenial := false
			for _, message := range calls[1].messages {
				if message.Role == "tool" && strings.Contains(strings.ToLower(message.Content), "denied") {
					foundDenial = true
				}
			}
			if !foundDenial {
				t.Fatalf("model did not receive a denied tool result: %#v", calls[1].messages)
			}
		})
	}
}

func TestLocalRepairPathGuardDeniesEscapeShapesAndPatchMove(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	outsideRoot := t.TempDir()
	outsideFile := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "outside-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatalf("newLocalRepairPathGuard() error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(root, "README.md"),
		"../README.md",
		"outside-link",
		".git/config",
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := guard.validate(path, false); err == nil {
				t.Fatalf("validate(%q) error = nil", path)
			}
		})
	}

	registry := newLocalRepairToolRegistry(guard)
	patchTool, ok := registry.Get("apply_patch")
	if !ok {
		t.Fatal("local repair apply_patch tool is unavailable")
	}
	result := patchTool.Execute(t.Context(), map[string]any{
		"patch": "*** Begin Patch\n" +
			"*** Update File: README.md\n" +
			"*** Move to: .git/moved\n" +
			"@@\n-before\n+after\n*** End Patch",
	})
	if result == nil || !result.IsError {
		t.Fatalf("apply_patch move result = %#v, want denial", result)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "README.md"))
	if readErr != nil || string(content) != "before\n" {
		t.Fatalf("README.md changed before denied move: content=%q error=%v", content, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".git", "moved")); !os.IsNotExist(statErr) {
		t.Fatalf("Git move destination exists after denial: %v", statErr)
	}
}

func TestLocalRepairRunnerRejectsInvalidProviderToolCallsBeforeExecution(t *testing.T) {
	tests := []struct {
		name       string
		toolCalls  []providers.ToolCall
		errorMatch string
	}{
		{
			name: "unknown tool",
			toolCalls: []providers.ToolCall{
				localRepairTestToolCall("unknown", "exec", map[string]any{"command": "touch escaped"}),
			},
			errorMatch: "invalid tool call",
		},
		{
			name: "duplicate tool call IDs",
			toolCalls: []providers.ToolCall{
				localRepairTestToolCall("duplicate", "edit_file", map[string]any{
					"path": "README.md", "old_text": "before\n", "new_text": "first\n",
				}),
				localRepairTestToolCall("duplicate", "apply_patch", map[string]any{
					"patch": "*** Begin Patch\n*** Add File: duplicate.txt\n+created\n*** End Patch",
				}),
			},
			errorMatch: "duplicate tool call ID",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pin, workspace, root := newLocalRepairTestWorkspace(t)
			acquirer := &localRepairTestAcquirer{workspace: workspace}
			provider := &localRepairTestProvider{
				handler: func(
					_ int,
					_ []providers.Message,
					_ []providers.ToolDefinition,
					_ string,
					_ map[string]any,
				) (*providers.LLMResponse, error) {
					return &providers.LLMResponse{ToolCalls: test.toolCalls}, nil
				},
			}

			_, err := newLocalRepairTestRunner(t, acquirer, provider, 3).Run(
				t.Context(),
				localRepairTestRunRequest(pin),
			)
			if err == nil || !strings.Contains(err.Error(), test.errorMatch) {
				t.Fatalf("Run() error = %v, want substring %q", err, test.errorMatch)
			}
			if len(provider.Calls()) != 1 {
				t.Fatalf("provider calls = %d, want 1", len(provider.Calls()))
			}
			if len(acquirer.Calls()) != 2 {
				t.Fatalf("AcquirePinned() calls = %d, want preflight and postflight", len(acquirer.Calls()))
			}
			content, readErr := os.ReadFile(filepath.Join(root, "README.md"))
			if readErr != nil || string(content) != "before\n" {
				t.Fatalf("README.md mutated before response rejection: content=%q error=%v", content, readErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, "duplicate.txt")); !os.IsNotExist(statErr) {
				t.Fatalf("duplicate.txt exists before response rejection: %v", statErr)
			}
		})
	}
}

func TestLocalRepairProviderStrictlyValidatesRawToolArgumentsAndMetadata(t *testing.T) {
	valid, err := cloneAndValidateLocalRepairResponse(&providers.LLMResponse{
		ToolCalls: []providers.ToolCall{{
			ID: "function-only",
			Function: &providers.FunctionCall{
				Name:      "read_file",
				Arguments: `{"path":"README.md"}`,
			},
		}},
	})
	if err != nil {
		t.Fatalf("valid function-only response error = %v", err)
	}
	if len(valid.ToolCalls) != 1 || valid.ToolCalls[0].Name != "read_file" ||
		valid.ToolCalls[0].Arguments["path"] != "README.md" {
		t.Fatalf("valid function-only response = %#v", valid)
	}

	tests := []struct {
		name     string
		response *providers.LLMResponse
		match    string
	}{
		{
			name: "invalid function JSON",
			response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
				ID: "invalid-json",
				Function: &providers.FunctionCall{
					Name: "read_file", Arguments: `{"path":`,
				},
			}}},
			match: "invalid tool arguments",
		},
		{
			name: "conflicting argument representations",
			response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
				ID:        "conflict",
				Name:      "read_file",
				Arguments: map[string]any{"path": "README.md"},
				Function: &providers.FunctionCall{
					Name: "read_file", Arguments: `{"path":"other.md"}`,
				},
			}}},
			match: "conflicting tool arguments",
		},
		{
			name: "oversized Google signature",
			response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
				ID:        "signature",
				Name:      "read_file",
				Arguments: map[string]any{"path": "README.md"},
				ExtraContent: &providers.ExtraContent{Google: &providers.GoogleExtra{
					ThoughtSignature: strings.Repeat("s", maxLocalRepairAnswerBytes+1),
				}},
			}}},
			match: "metadata is too large",
		},
		{
			name: "oversized reasoning detail",
			response: &providers.LLMResponse{ReasoningDetails: []protocoltypes.ReasoningDetail{{
				Type: "text", Text: strings.Repeat("r", maxLocalRepairToolArguments+1),
			}}},
			match: "metadata is invalid or too large",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := cloneAndValidateLocalRepairResponse(test.response)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("response error = %v, want substring %q", err, test.match)
			}
		})
	}
}

func TestLocalRepairRunnerIterationLimit(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{
		handler: func(
			_ int,
			_ []providers.Message,
			_ []providers.ToolDefinition,
			_ string,
			_ map[string]any,
		) (*providers.LLMResponse, error) {
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				localRepairTestToolCall("read", "read_file", map[string]any{"path": "README.md"}),
			}}, nil
		},
	}

	result, err := newLocalRepairTestRunner(t, acquirer, provider, 1).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if !errors.Is(err, ErrLocalRepairLimit) {
		t.Fatalf("Run() error = %v, want ErrLocalRepairLimit", err)
	}
	if result.Iterations != 1 || result.WorkspaceID != workspace.ID {
		t.Fatalf("Run() result = %#v", result)
	}
	if len(provider.Calls()) != 1 || len(acquirer.Calls()) != 2 {
		t.Fatalf("calls after limit: provider=%d workspace=%d", len(provider.Calls()), len(acquirer.Calls()))
	}
}

func TestLocalRepairRunnerRejectsNilProviderResponse(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{
		handler: func(
			_ int,
			_ []providers.Message,
			_ []providers.ToolDefinition,
			_ string,
			_ map[string]any,
		) (*providers.LLMResponse, error) {
			return nil, nil
		},
	}

	_, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if err == nil || !strings.Contains(err.Error(), "returned no response") {
		t.Fatalf("Run() error = %v, want nil-response rejection", err)
	}
	if len(provider.Calls()) != 1 || len(acquirer.Calls()) != 2 {
		t.Fatalf("calls after nil response: provider=%d workspace=%d", len(provider.Calls()), len(acquirer.Calls()))
	}
}

func TestLocalRepairRunnerRejectsPostflightIdentityMismatch(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{
		workspace: workspace,
		transform: func(call int, info gitworkspace.WorkspaceInfo) gitworkspace.WorkspaceInfo {
			if call == 2 {
				info.ID = "workspace-two"
			}
			return info
		},
	}
	provider := &localRepairTestProvider{}

	result, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if !errors.Is(err, ErrLocalRepairPin) {
		t.Fatalf("Run() error = %v, want ErrLocalRepairPin", err)
	}
	if result.Content != "done" || result.WorkspaceID != workspace.ID {
		t.Fatalf("Run() result = %#v", result)
	}
	if len(provider.Calls()) != 1 || len(acquirer.Calls()) != 2 {
		t.Fatalf(
			"calls after identity mismatch: provider=%d workspace=%d",
			len(provider.Calls()),
			len(acquirer.Calls()),
		)
	}
}

func TestLocalRepairRunnerRejectsPostflightLockEpochMismatch(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{
		workspace: workspace,
		transform: func(call int, info gitworkspace.WorkspaceInfo) gitworkspace.WorkspaceInfo {
			if call == 2 {
				info.LockedBy.LockedAt = info.LockedBy.LockedAt.Add(time.Second)
			}
			return info
		},
	}
	provider := &localRepairTestProvider{}

	_, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if !errors.Is(err, ErrLocalRepairPin) {
		t.Fatalf("Run() error = %v, want ErrLocalRepairPin", err)
	}
}

func TestLocalRepairRunnerPreservesToolThoughtSignatures(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{}
	provider.handler = func(
		index int,
		_ []providers.Message,
		_ []providers.ToolDefinition,
		_ string,
		_ map[string]any,
	) (*providers.LLMResponse, error) {
		if index == 0 {
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
				ID:               "signed-call",
				Name:             "read_file",
				Arguments:        map[string]any{"path": "README.md"},
				ThoughtSignature: "top-signature",
				Function: &providers.FunctionCall{
					Name:             "read_file",
					ThoughtSignature: "function-signature",
				},
				ExtraContent: &providers.ExtraContent{
					Google: &providers.GoogleExtra{
						ThoughtSignature: "google-signature",
					},
					ToolFeedbackExplanation: "explanation",
				},
			}}}, nil
		}
		return &providers.LLMResponse{Content: "done"}, nil
	}

	_, err := newLocalRepairTestRunner(t, acquirer, provider, 3).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calls := provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	var preserved *providers.ToolCall
	for _, message := range calls[1].messages {
		if message.Role == "assistant" && len(message.ToolCalls) == 1 {
			call := message.ToolCalls[0]
			preserved = &call
			break
		}
	}
	if preserved == nil {
		t.Fatalf("second request has no assistant tool call: %#v", calls[1].messages)
	}
	if preserved.ThoughtSignature != "top-signature" || preserved.Function == nil ||
		preserved.Function.ThoughtSignature != "function-signature" ||
		preserved.ExtraContent == nil || preserved.ExtraContent.Google == nil ||
		preserved.ExtraContent.Google.ThoughtSignature != "google-signature" ||
		preserved.ExtraContent.ToolFeedbackExplanation != "explanation" {
		t.Fatalf("tool signature metadata was not preserved: %#v", preserved)
	}
}
