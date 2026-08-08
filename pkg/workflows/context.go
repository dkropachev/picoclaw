package workflows

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/session"
)

type Delivery struct {
	Channel          string            `json:"channel,omitempty"`
	ChatID           string            `json:"chat_id,omitempty"`
	TopicID          string            `json:"topic_id,omitempty"`
	ThreadTS         string            `json:"thread_ts,omitempty"`
	MessageID        string            `json:"message_id,omitempty"`
	ReplyToMessageID string            `json:"reply_to_message_id,omitempty"`
	ReplyHandles     map[string]string `json:"reply_handles,omitempty"`
}

type EventContext struct {
	Channel map[string]any `json:"channel,omitempty"`
	Chat    map[string]any `json:"chat,omitempty"`
	Topic   map[string]any `json:"topic,omitempty"`
	Sender  map[string]any `json:"sender,omitempty"`
	Message map[string]any `json:"message,omitempty"`
	ReplyTo map[string]any `json:"reply_to,omitempty"`
	Raw     map[string]any `json:"raw,omitempty"`
}

type ExecutionContext struct {
	Inputs       map[string]any           `json:"inputs,omitempty"`
	Secrets      map[string]string        `json:"-"`
	Event        map[string]any           `json:"event,omitempty"`
	Session      string                   `json:"session,omitempty"`
	Delivery     Delivery                 `json:"delivery,omitempty"`
	Steps        map[string]StepExecution `json:"steps,omitempty"`
	Needs        map[string]JobExecution  `json:"needs,omitempty"`
	WorkspaceDir string                   `json:"workspace_dir,omitempty"`
	WorkflowRef  string                   `json:"workflow_ref,omitempty"`
	RunID        string                   `json:"run_id,omitempty"`
	JobID        string                   `json:"job_id,omitempty"`
	StepID       string                   `json:"step_id,omitempty"`

	// Private workflow-root state is intentionally unavailable to external
	// FunctionRunner implementations. Compiler-generated steps may read values
	// only through the private expression root, and read-only agent steps receive
	// the separately frozen session snapshot.
	privateValues         map[string]any
	frozenReadOnlySession *FrozenReadOnlySession
}

// PrivateRootRequest is internal invocation context for a trusted,
// compiler-generated workflow. Values are frozen before durable run creation
// and never projected through ordinary Run JSON. ReadOnlySession, when set,
// is captured exactly once before creation and reused by resume and retry.
//
// This is local persisted context, not a secret vault: a trusted workflow can
// deliberately declassify a value by rendering it into a human task.
type PrivateRootRequest struct {
	Values          map[string]any
	ReadOnlySession *ReadOnlySessionRef

	// privateValuesRevision binds the exact normalized compiler-owned values
	// while still allowing the caller to attach the required session reference.
	// It is deliberately unexported so serialization cannot recreate admission
	// authority.
	privateValuesRevision string
}

// ReadOnlySessionRef selects one exact existing session owned by AgentID.
// Session is a local capability and is never persisted outside the private
// frozen root or emitted through workflow observations. ExpectedRevision, when
// non-empty, fences capture to the exact store snapshot observed by the caller.
// An empty ExpectedRevision preserves unfenced capture for legacy local callers.
type ReadOnlySessionRef struct {
	AgentID          string `json:"AgentID"`
	Session          string `json:"Session"`
	ExpectedRevision string `json:"-"`
}

// FrozenReadOnlySession is the detached evidence supplied to every matching
// read-only agent step in one private workflow root.
type FrozenReadOnlySession struct {
	AgentID         string                  `json:"agent_id"`
	Snapshot        session.SessionSnapshot `json:"snapshot"`
	HistoryRevision string                  `json:"history_revision"`
	FrozenMedia     media.FrozenSet         `json:"frozen_media"`
}

// ReadOnlySessionCapturer is the optional AgentRunner capability used during
// private-root admission. Capture must return a graph-detached exact snapshot
// and must not create or mutate a session.
type ReadOnlySessionCapturer interface {
	CaptureReadOnlySession(
		ctx context.Context,
		ref ReadOnlySessionRef,
	) (*FrozenReadOnlySession, error)
}

type StepExecution struct {
	ID      string         `json:"id,omitempty"`
	Outputs map[string]any `json:"outputs,omitempty"`
	Status  string         `json:"status,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type JobExecution struct {
	ID      string         `json:"id,omitempty"`
	Outputs map[string]any `json:"outputs,omitempty"`
	Status  string         `json:"status,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type ToolRunner interface {
	RunTool(ctx context.Context, req ToolRequest) (map[string]any, error)
}

type ToolRequest struct {
	Name      string
	Args      map[string]any
	Session   string
	Delivery  Delivery
	AgentID   string
	MessageID string
	MCP       bool
	MCPServer string
	MCPTool   string
}

type AgentRunner interface {
	RunAgent(ctx context.Context, req AgentRequest) (map[string]any, error)
}

const (
	AgentToolsInherit     = "inherit"
	AgentToolsNone        = "none"
	AgentSessionEphemeral = "ephemeral"
	AgentSessionPrivate   = "private"
)

type AgentRequest struct {
	AgentID          string
	Message          string
	Prompt           string
	Context          string
	Session          string
	EphemeralSession bool
	History          string
	Cache            string
	Tools            string
	Delivery         Delivery
	Inputs           map[string]any
	MessageID        string
	Output           *AgentOutputContract
	Managed          any
	Scope            any
	// PrivateContext marks an agent step whose inputs belong to a private
	// workflow root. Runners must keep provider diagnostics out of ordinary
	// runtime events and shared health-state error fields for this request.
	PrivateContext bool
	// IsolatedSystemPrompt replaces the configured agent's default system,
	// bootstrap, memory, skill, contributor, and dynamic context for one trusted
	// private ephemeral request. The workflow runner rejects it on every broader
	// session or authority profile.
	IsolatedSystemPrompt string
	// FrozenReadOnlySession bypasses live session lookup for one private-root
	// read-only decision. Session remains empty so its local capability key
	// cannot enter ordinary workflow output or routing context.
	FrozenReadOnlySession *FrozenReadOnlySession
}

type FunctionRunner interface {
	RunFunction(ctx context.Context, name string, args map[string]any, exec ExecutionContext) (map[string]any, error)
}

type FunctionFunc func(context.Context, map[string]any, ExecutionContext) (map[string]any, error)

type FunctionRegistry struct {
	funcs map[string]FunctionFunc
}

func NewFunctionRegistry() *FunctionRegistry {
	return &FunctionRegistry{funcs: make(map[string]FunctionFunc)}
}

func (r *FunctionRegistry) Register(name string, fn FunctionFunc) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("function name is required")
	}
	if fn == nil {
		return fmt.Errorf("function %q is nil", name)
	}
	r.funcs[name] = fn
	return nil
}

func (r *FunctionRegistry) RunFunction(
	ctx context.Context,
	name string,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("function registry not configured")
	}
	fn, ok := r.funcs[name]
	if !ok {
		return nil, fmt.Errorf("function %q not found", name)
	}
	return fn(ctx, args, exec)
}
