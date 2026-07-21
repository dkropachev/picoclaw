package workflows

import (
	"context"
	"fmt"
	"strings"
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
}

type AgentRunner interface {
	RunAgent(ctx context.Context, req AgentRequest) (map[string]any, error)
}

type AgentRequest struct {
	AgentID   string
	Message   string
	Prompt    string
	Context   string
	Session   string
	History   string
	Cache     string
	Delivery  Delivery
	Inputs    map[string]any
	MessageID string
	Output    *AgentOutputContract
	Managed   any
	Scope     any
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
