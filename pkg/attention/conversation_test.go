package attention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	conversationTestResponseDomain = "picoclaw.attention-test.response.v1"
	conversationTestTokenDomain    = "picoclaw.attention-test.token.v1"
)

func TestConversationEngineProjectsRespondsAndReplays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	snapshot := PolicySnapshot{
		Revision: "conversation-v1",
		Global: []workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic, When: "true",
			Title: "Choose a bounded option", Questions: []any{"Proceed?"},
		}},
	}
	prepared, err := PrepareSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	runs := workflows.NewFileRunStore(workspace)
	bindings := &conversationTestDecisionBinding{links: make(map[string]string)}
	runner, err := NewPrivateRunner(PrivateRunnerConfig{
		Executor: &workflows.Executor{WorkspaceDir: workspace, Store: runs},
		Runs:     runs,
		Policies: PolicySourceFunc(func(
			ctx context.Context,
			_ PolicySelector,
			use PolicyUse,
		) error {
			return use(ctx, snapshot)
		}),
		Decisions: bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := CanonicalDecisionKey(map[string]any{"case": "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	launched, err := runner.Launch(ctx, PrivateLaunchRequest{
		DecisionKey: key,
		Policy:      prepared,
	})
	if err != nil || launched.Status != workflows.RunStatusWaiting {
		t.Fatalf("Launch() = (%#v, %v)", launched, err)
	}

	originalStore := workflows.NewFileRunStore(t.TempDir())
	originalExecutor := &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        originalStore,
	}
	engine, err := NewConversationEngine(ConversationEngineConfig{
		RunStore:         runs,
		Executor:         originalExecutor,
		MaxResponseBytes: 1024,
		ResponseIDDomain: conversationTestResponseDomain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if originalExecutor.Store != originalStore {
		t.Fatal("NewConversationEngine mutated its caller-owned executor")
	}
	input := ConversationInput{
		CaseVersion: 7,
		RunID:       launched.RunID,
		Token:       conversationTestToken,
	}
	initial, err := engine.Project(ctx, input)
	if err != nil || initial.Status != ConversationStatusWaiting ||
		!initial.CanRespond || len(initial.Turns) != 1 ||
		!ValidConversationResponseToken(initial.Turns[0].ResponseToken) {
		t.Fatalf("Project() = (%#v, %v)", initial, err)
	}
	token := initial.Turns[0].ResponseToken
	completed, err := engine.Respond(ctx, ConversationResponse{
		ConversationInput:   input,
		ExpectedCaseVersion: input.CaseVersion,
		ResponseToken:       token,
		Response:            "  Proceed locally.  ",
	})
	if err != nil || completed.Status != ConversationStatusCompleted ||
		completed.CanRespond || len(completed.Turns) != 1 ||
		completed.Turns[0].Response != "Proceed locally." ||
		completed.Turns[0].ResponseToken != "" {
		t.Fatalf("Respond() = (%#v, %v)", completed, err)
	}
	replayed, err := engine.Respond(ctx, ConversationResponse{
		ConversationInput:   input,
		ExpectedCaseVersion: input.CaseVersion,
		ResponseToken:       token,
		Response:            "Proceed locally.",
	})
	if err != nil || replayed.Status != ConversationStatusCompleted {
		t.Fatalf("Respond(replay) = (%#v, %v)", replayed, err)
	}
	if _, err = engine.Respond(ctx, ConversationResponse{
		ConversationInput:   input,
		ExpectedCaseVersion: input.CaseVersion,
		ResponseToken:       token,
		Response:            "Choose differently.",
	}); !errors.Is(err, ErrConversationConflict) {
		t.Fatalf("Respond(altered replay) error = %v, want conflict", err)
	}

	tampered := &conversationTestTaskStore{
		FileRunStore: runs,
		mutate: func(task *workflows.WorkflowHumanTask) {
			task.Response = strings.Repeat("x", 1025)
			task.ResponseID = ConversationResponseID(
				conversationTestResponseDomain,
				token,
				task.Response.(string),
			)
		},
	}
	tamperedEngine, err := NewConversationEngine(ConversationEngineConfig{
		RunStore: tampered, MaxResponseBytes: 1024,
		ResponseIDDomain: conversationTestResponseDomain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tamperedEngine.Project(ctx, input); !errors.Is(
		err,
		ErrConversationUnavailable,
	) {
		t.Fatalf("Project(tampered response) error = %v, want unavailable", err)
	}
}

func TestConversationEngineRejectsInvalidConfigurationAndFences(t *testing.T) {
	t.Parallel()
	runs := workflows.NewFileRunStore(t.TempDir())
	for _, config := range []ConversationEngineConfig{
		{},
		{RunStore: runs, MaxResponseBytes: 1},
		{
			RunStore: runs, MaxResponseBytes: workflows.MaxHumanTaskPayloadBytes + 1,
			ResponseIDDomain: conversationTestResponseDomain,
		},
		{RunStore: runs, MaxResponseBytes: 1, ResponseIDDomain: " domain "},
	} {
		if engine, err := NewConversationEngine(config); err == nil || engine != nil {
			t.Fatalf("NewConversationEngine(%#v) = (%#v, %v)", config, engine, err)
		}
	}
	if !ValidConversationResponseToken("sha256:"+strings.Repeat("a", 64)) ||
		ValidConversationResponseToken("sha256:"+strings.Repeat("A", 64)) {
		t.Fatal("response-token canonical validation mismatch")
	}
	first := ConversationResponseID("domain-one", "token", "answer")
	second := ConversationResponseID("domain-two", "token", "answer")
	if first == second || !ValidConversationResponseToken(first) ||
		!ValidConversationResponseToken(second) {
		t.Fatalf("domain-separated response IDs = (%q, %q)", first, second)
	}
	engine, err := NewConversationEngine(ConversationEngineConfig{
		RunStore: runs, MaxResponseBytes: 16,
		ResponseIDDomain: conversationTestResponseDomain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Respond(context.Background(), ConversationResponse{
		ConversationInput: ConversationInput{
			CaseVersion: 1, RunID: "wr_" + strings.Repeat("0", 32),
			Token: conversationTestToken,
		},
		ExpectedCaseVersion: 2,
		ResponseToken:       first,
		Response:            "answer",
	}); !errors.Is(err, ErrConversationConflict) {
		t.Fatalf("Respond(stale case) error = %v, want conflict", err)
	}
}

type conversationTestDecisionBinding struct {
	mu    sync.Mutex
	links map[string]string
}

func (binding *conversationTestDecisionBinding) Find(
	_ context.Context,
	key string,
) (string, bool, error) {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	runID, ok := binding.links[key]
	return runID, ok, nil
}

func (binding *conversationTestDecisionBinding) Admit(
	ctx context.Context,
	key string,
	create func(context.Context) error,
) (string, bool, error) {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if runID, ok := binding.links[key]; ok {
		return runID, true, nil
	}
	runID, err := RunIDForDecisionKey(key)
	if err != nil {
		return "", false, err
	}
	if err = create(ctx); err != nil {
		return "", false, err
	}
	binding.links[key] = runID
	return runID, false, nil
}

type conversationTestTaskStore struct {
	*workflows.FileRunStore
	mutate func(*workflows.WorkflowHumanTask)
}

func (store *conversationTestTaskStore) ListHumanTasks(
	ctx context.Context,
	runID string,
) ([]workflows.WorkflowHumanTask, error) {
	tasks, err := store.FileRunStore.ListHumanTasks(ctx, runID)
	if err != nil {
		return nil, err
	}
	for index := range tasks {
		store.mutate(&tasks[index])
	}
	return tasks, nil
}

func conversationTestToken(
	task workflows.WorkflowHumanTask,
	waitingRevision uint64,
) (string, error) {
	digest := sha256.New()
	WriteConversationHashField(digest, []byte(conversationTestTokenDomain))
	WriteConversationHashField(digest, []byte(task.RunID))
	WriteConversationHashField(digest, []byte(task.ID))
	WriteConversationHashUint64(digest, waitingRevision)
	WriteConversationHashField(digest, []byte(task.InputHash))
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
