package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

// GateWorkingContextRequest is the exact bounded PR-workspace projection made
// available to a configured ai_working_context stage. The binder turns it into
// a protected agent-owned session and returns an exact revision fence; the
// workflow executor then freezes that revision before creating the run.
type GateWorkingContextRequest struct {
	WorkspaceID      string
	WorkspaceVersion int64
	AgentID          string
	Context          PRContextBundle
}

type GateWorkingContextBinder interface {
	Bind(ctx context.Context, request GateWorkingContextRequest) (workflows.ReadOnlySessionRef, error)
}

// GateWorkingContextRuntimeAcquire pins the runtime generation while the
// protected projection is written and returns the selected agent's exact
// session store.
type GateWorkingContextRuntimeAcquire func(
	ctx context.Context,
	agentID string,
) (context.Context, session.SessionStore, func(), error)

// SessionGateWorkingContextBinder stores one derived, protected session per PR
// workspace and agent. The aggregate remains authoritative; this session is a
// replaceable read-only view used only by private workflow gates.
type SessionGateWorkingContextBinder struct {
	Acquire GateWorkingContextRuntimeAcquire
	mu      sync.Mutex
}

type exactGateWorkingContextStore interface {
	session.SnapshotReader
	session.SnapshotReplacer
	session.ScopeAdmitter
}

func (binder *SessionGateWorkingContextBinder) Bind(
	ctx context.Context,
	request GateWorkingContextRequest,
) (workflows.ReadOnlySessionRef, error) {
	if binder == nil || binder.Acquire == nil ||
		!validOpaqueID(request.WorkspaceID, "devw_") || request.WorkspaceVersion <= 0 ||
		request.Context.WorkspaceID != request.WorkspaceID ||
		request.AgentID != routing.NormalizeAgentID(request.AgentID) ||
		!routing.IsCanonicalAgentID(request.AgentID) {
		return workflows.ReadOnlySessionRef{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtimeCtx, rawStore, release, acquireErr := binder.Acquire(ctx, request.AgentID)
	if acquireErr != nil {
		if release != nil {
			release()
		}
		return workflows.ReadOnlySessionRef{}, fmt.Errorf("acquire PR gate working context: %w", acquireErr)
	}
	if runtimeCtx == nil || rawStore == nil || release == nil {
		if release != nil {
			release()
		}
		return workflows.ReadOnlySessionRef{}, errors.New("PR gate working-context runtime is unavailable")
	}
	defer release()
	if err := runtimeCtx.Err(); err != nil {
		return workflows.ReadOnlySessionRef{}, err
	}
	store, ok := rawStore.(exactGateWorkingContextStore)
	if !ok {
		return workflows.ReadOnlySessionRef{}, errors.New("PR gate working-context store lacks exact snapshot support")
	}
	history, historyErr := gateWorkingContextHistory(request)
	if historyErr != nil {
		return workflows.ReadOnlySessionRef{}, historyErr
	}

	// One process may start gates for the same aggregate concurrently before
	// either aggregate CAS commits. Serialize the derived-session replacement;
	// the returned revision still fences any cross-process or later mutation.
	binder.mu.Lock()
	defer binder.mu.Unlock()

	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    request.AgentID,
		Channel:    "review",
		Account:    routing.DefaultAccountID,
		Dimensions: []string{"pr_workspace"},
		Values:     map[string]string{"pr_workspace": request.WorkspaceID},
	}
	key := session.BuildSessionKey(scope)
	if _, err := store.AdmitSessionScope(runtimeCtx, session.SessionScopeAdmission{
		Key: key, Scope: session.CloneScope(&scope), Mode: session.ScopeAdmissionReview,
	}); err != nil {
		return workflows.ReadOnlySessionRef{}, fmt.Errorf("admit PR gate working context: %w", err)
	}
	previous, found, readErr := store.ReadSessionSnapshot(runtimeCtx, key)
	if readErr != nil {
		return workflows.ReadOnlySessionRef{}, fmt.Errorf("read PR gate working context: %w", readErr)
	}
	if !found || previous.Key != key || previous.Revision == "" ||
		!reflect.DeepEqual(previous.Scope, &scope) {
		return workflows.ReadOnlySessionRef{}, errors.New("PR gate working-context binding is invalid")
	}
	replacement := session.SessionSnapshotReplacement{
		Key: key, History: history, Scope: session.CloneScope(&scope),
		ExpectedRevision: previous.Revision,
	}
	if err := store.ReplaceSessionSnapshot(runtimeCtx, replacement); err != nil {
		return workflows.ReadOnlySessionRef{}, fmt.Errorf("replace PR gate working context: %w", err)
	}
	verified, found, verifyErr := store.ReadSessionSnapshot(runtimeCtx, key)
	if verifyErr != nil {
		return workflows.ReadOnlySessionRef{}, fmt.Errorf("verify PR gate working context: %w", verifyErr)
	}
	if !found || verified.Key != key || verified.Revision == "" ||
		verified.Revision == previous.Revision || !reflect.DeepEqual(verified.Scope, &scope) ||
		!equalGateWorkingContextHistory(verified.History, history) {
		return workflows.ReadOnlySessionRef{}, errors.New("PR gate working-context replacement did not persist exactly")
	}
	return workflows.ReadOnlySessionRef{
		AgentID: request.AgentID, Session: key, ExpectedRevision: verified.Revision,
	}, nil
}

func gateWorkingContextHistory(request GateWorkingContextRequest) ([]providers.Message, error) {
	bundle := request.Context
	messages := append([]Message(nil), bundle.Messages...)
	bundle.Messages = nil
	encoded, err := json.Marshal(struct {
		WorkspaceVersion int64           `json:"workspace_version"`
		Context          PRContextBundle `json:"context"`
	}{WorkspaceVersion: request.WorkspaceVersion, Context: bundle})
	if err != nil || len(encoded) > maxContextBundleBytes {
		return nil, errors.New("PR gate working context is invalid or too large")
	}
	history := make([]providers.Message, 0, len(messages)+1)
	history = append(history, providers.Message{
		Role: "user", Content: "PR WORKSPACE FACTS (UNTRUSTED DATA):\n" + string(encoded),
	})
	for _, message := range messages {
		createdAt := message.CreatedAt.UTC()
		history = append(history, providers.Message{
			Role: message.Role, Content: message.Content, CreatedAt: &createdAt,
		})
	}
	return history, nil
}

func equalGateWorkingContextHistory(left, right []providers.Message) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Role != right[index].Role || left[index].Content != right[index].Content ||
			!equalGateWorkingContextTime(left[index].CreatedAt, right[index].CreatedAt) {
			return false
		}
	}
	return true
}

func equalGateWorkingContextTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
