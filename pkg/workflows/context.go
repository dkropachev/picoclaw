package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/session"
)

var (
	ErrAgentCallNotAdmitted                   = errors.New("workflow agent provider call was not admitted")
	ErrManagedChildActivityNotRecorded        = errors.New("workflow managed child activity was not recorded")
	ErrManagedAssignmentCheckpointNotRecorded = errors.New(
		"workflow managed assignment checkpoint was not recorded",
	)
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
	workspaceCleanup      *workflowWorkspaceCleanup
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

// AgentUsage is the provider-reported token usage for one concrete model.
// It deliberately contains no request, response, account, or credential data.
type AgentUsage struct {
	Model            string `json:"model"`
	Reviewer         string `json:"reviewer,omitempty"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	CachedTokens     int    `json:"cached_tokens"`
	ReasoningTokens  int    `json:"reasoning_tokens,omitempty"`
	LatencyMillis    int64  `json:"latency_millis,omitempty"`
}

// AgentUsageObserver is called once for each completed provider response.
// Returning an error aborts the workflow agent execution.
type AgentUsageObserver func(AgentUsage) error

// AgentCallAdmission is checked immediately before each provider request.
// Returning an error prevents that request from being dispatched.
type AgentCallAdmission func() error

// AgentUsageEvent adds durable workflow identity to one provider usage event.
type AgentUsageEvent struct {
	RunID  string     `json:"run_id"`
	JobID  string     `json:"job_id"`
	StepID string     `json:"step_id"`
	Usage  AgentUsage `json:"usage"`
}

// AgentUsageEventObserver observes provider usage with workflow identity.
type AgentUsageEventObserver func(AgentUsageEvent) error

// StepActivityEvent identifies a workflow step at the boundary immediately
// before its tool, native function, or agent work begins.
type StepActivityEvent struct {
	RunID  string `json:"run_id"`
	JobID  string `json:"job_id"`
	StepID string `json:"step_id"`
	Uses   string `json:"uses"`
}

// StepActivityObserver projects real workflow activity to an owning control
// plane. Returning an error prevents the step from starting.
type StepActivityObserver func(StepActivityEvent) error

// ManagedChildActivityPhase identifies a managed child's lifecycle boundary.
type ManagedChildActivityPhase string

const (
	ManagedChildStarted   ManagedChildActivityPhase = "started"
	ManagedChildCompleted ManagedChildActivityPhase = "completed"
)

// ManagedChildActivity identifies one concrete managed child without exposing
// its prompt, response, or repository content.
type ManagedChildActivity struct {
	Phase                 ManagedChildActivityPhase `json:"phase"`
	Index                 int                       `json:"index"`
	Total                 int                       `json:"total"`
	AssignmentID          string                    `json:"assignment_id,omitempty"`
	FocusID               string                    `json:"focus_id,omitempty"`
	Label                 string                    `json:"label,omitempty"`
	ModelAlias            string                    `json:"model_alias,omitempty"`
	ScopeCount            int                       `json:"scope_count"`
	EstimatedPromptTokens int                       `json:"estimated_prompt_tokens,omitempty"`
	EstimatedOutputTokens int                       `json:"estimated_output_tokens,omitempty"`
	EstimatedCostUSD      float64                   `json:"estimated_cost_usd,omitempty"`
	PriceKnown            bool                      `json:"price_known,omitempty"`
	Success               bool                      `json:"success"`
}

// ManagedChildActivityObserver receives bounded managed child lifecycle data.
type ManagedChildActivityObserver func(ManagedChildActivity) error

// ManagedChildActivityEvent adds durable workflow identity to one managed
// child lifecycle event.
type ManagedChildActivityEvent struct {
	RunID  string `json:"run_id"`
	JobID  string `json:"job_id"`
	StepID string `json:"step_id"`
	ManagedChildActivity
}

// ManagedChildActivityEventObserver receives managed child data with workflow identity.
type ManagedChildActivityEventObserver func(ManagedChildActivityEvent) error

// ManagedAssignmentDispatchEvent identifies one exact planned assignment at
// the last admission boundary before a provider request. Scope contains only
// detached file references; it never contains repository content or a source
// capability. The same event may be delivered again for a structured-output
// repair, and observers must therefore be idempotent.
type ManagedAssignmentDispatchEvent struct {
	AssignmentID  string `json:"assignment_id"`
	FocusID       string `json:"focus_id"`
	Index         int    `json:"index"`
	Total         int    `json:"total"`
	Label         string `json:"label,omitempty"`
	ReviewerModel string `json:"reviewer_model,omitempty"`
	Model         string `json:"model,omitempty"`
	Required      bool   `json:"required"`
	Scope         []any  `json:"scope"`
}

// ManagedAssignmentDispatchObserver verifies that an exact assignment remains
// dispatchable immediately before each provider request.
type ManagedAssignmentDispatchObserver func(ManagedAssignmentDispatchEvent) error

// ManagedAssignmentCheckpointEvent carries one independently validated child
// result to its durable domain checkpoint. Output is a detached structured JSON
// value. OutputDigest binds the raw child response without exposing it, while
// CheckpointDigest binds the assignment, exact scope, model, and output together.
type ManagedAssignmentCheckpointEvent struct {
	ManagedAssignmentDispatchEvent
	Output           any    `json:"output"`
	OutputDigest     string `json:"output_digest"`
	CheckpointDigest string `json:"checkpoint_digest"`
}

// ManagedAssignmentCheckpointObserver durably records one successful
// assignment. Returning nil is the managed runner's acknowledgement that the
// result reached its atomic domain checkpoint.
type ManagedAssignmentCheckpointObserver func(ManagedAssignmentCheckpointEvent) error

type AgentCallAdmissionEvent struct {
	RunID  string `json:"run_id"`
	JobID  string `json:"job_id"`
	StepID string `json:"step_id"`
}

type AgentCallAdmissionEventObserver func(AgentCallAdmissionEvent) error

type RepositoryReviewModelProfile struct {
	Revision               string
	AccountRef             string
	ReviewerModels         []string
	IncludeDefaultReviewer bool
	MaxContentBytes        int
}

// RepositoryReviewProfileResolver binds incremental review checkpoints to the
// effective configured model graph without exposing provider credentials.
type RepositoryReviewProfileResolver interface {
	ResolveRepositoryReviewProfile(
		ctx context.Context,
		agentID string,
		requestedAccountRef string,
		requestedReviewerModels []string,
	) (RepositoryReviewModelProfile, error)
}

const (
	AgentToolsInherit     = "inherit"
	AgentToolsNone        = "none"
	AgentSessionEphemeral = "ephemeral"
	AgentSessionPrivate   = "private"
	AgentSessionSource    = "source"
)

// AgentSourceCapture asks a trusted private AI adapter to retain the exact
// finding-producing turn as a protected, read-only source session.
type AgentSourceCapture struct {
	ExecutionID string
	WorkspaceID string
	Binding     string
}

type AgentRequest struct {
	AgentID string
	// AccountRef optionally selects one configured account or account router for
	// this isolated workflow call. Empty inherits the configured agent account.
	AccountRef string
	// Model optionally selects one configured model alias for this isolated
	// workflow call. It does not accept provider credentials or concrete account
	// overrides; normal alias/account resolution remains authoritative.
	Model            string
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
	ScopeContent     string
	// SuppressDefaultContext is a reduction-only trusted workflow profile for
	// evidence-bound no-tool reviews. It omits mutable bootstrap, memory, skill,
	// contributor, and dynamic prompt context while retaining the supplied task.
	SuppressDefaultContext bool
	// ReviewSystemPrompt is the fixed trusted policy paired with the suppressed
	// built-in repository-review profile. Repository bytes remain only in Scope.
	ReviewSystemPrompt string
	// PrivateContext marks an agent step whose inputs belong to a private
	// workflow root. Runners must keep provider diagnostics out of ordinary
	// runtime events and shared health-state error fields for this request.
	PrivateContext bool
	SourceCapture  *AgentSourceCapture
	// IsolatedSystemPrompt replaces the configured agent's default system,
	// bootstrap, memory, skill, contributor, and dynamic context for one trusted
	// private ephemeral request. The workflow runner rejects it on every broader
	// session or authority profile.
	IsolatedSystemPrompt string
	// FrozenReadOnlySession bypasses live session lookup for one private-root
	// read-only decision. Session remains empty so its local capability key
	// cannot enter ordinary workflow output or routing context.
	FrozenReadOnlySession *FrozenReadOnlySession
	// UsageObserver receives provider-reported token counts and model provenance
	// only. It must not be used to expose prompts, responses, or account data.
	UsageObserver AgentUsageObserver
	// ManagedChildObserver receives bounded start/completion metadata for each
	// managed child. It never receives prompts, responses, or scope content.
	ManagedChildObserver ManagedChildActivityObserver
	// ManagedAssignmentDispatch is checked immediately before every provider
	// request made for an explicit managed assignment, including repairs.
	ManagedAssignmentDispatch ManagedAssignmentDispatchObserver
	// ManagedAssignmentCheckpoint receives each valid explicit assignment output
	// synchronously before the child is reported complete.
	ManagedAssignmentCheckpoint ManagedAssignmentCheckpointObserver
	// AssignmentTimeoutSeconds is one fixed wall-clock budget shared by an
	// explicit assignment's initial provider request and all output repairs. Zero
	// retains the caller's existing context deadline without adding another one.
	AssignmentTimeoutSeconds int
	// CallAdmission is checked before every provider request, including queued
	// managed children, fallbacks, retries, and structured-output repairs.
	CallAdmission AgentCallAdmission
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
