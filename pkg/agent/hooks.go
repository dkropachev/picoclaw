package agent

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	defaultHookObserverTimeout    = 500 * time.Millisecond
	defaultHookInterceptorTimeout = 5 * time.Second
	defaultHookApprovalTimeout    = 60 * time.Second
	hookObserverBufferSize        = 64
)

type HookAction string

const (
	HookActionContinue HookAction = "continue"
	HookActionModify   HookAction = "modify"
	// HookActionRespond lets a trusted BeforeTool hook provide a synthetic
	// result. Agent Pipeline still requires exact offered/registered authority,
	// central ToolPolicy allow, and approval before exposing that result.
	HookActionRespond   HookAction = "respond"
	HookActionDenyTool  HookAction = "deny_tool"
	HookActionAbortTurn HookAction = "abort_turn"
	HookActionHardAbort HookAction = "hard_abort"
)

type HookDecision struct {
	Action HookAction `json:"action"`
	Reason string     `json:"reason,omitempty"`
}

func (d HookDecision) normalizedAction() HookAction {
	if d.Action == "" {
		return HookActionContinue
	}
	return d.Action
}

type ApprovalDecision struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

type HookSource uint8

const (
	HookSourceInProcess HookSource = iota
	HookSourceProcess
)

func (source HookSource) String() string {
	switch source {
	case HookSourceInProcess:
		return "in_process"
	case HookSourceProcess:
		return "process"
	default:
		return "unknown"
	}
}

// HookTrust is independent of HookSource. Source describes transport and
// ordering; trust decides whether an interceptor may change execution data or
// synthesize a tool response. The zero value is deliberately untrusted.
type HookTrust uint8

const (
	HookTrustUntrusted HookTrust = iota
	HookTrustTrusted
)

func hookTrustFromBool(trusted bool) HookTrust {
	if trusted {
		return HookTrustTrusted
	}
	return HookTrustUntrusted
}

type HookRegistration struct {
	Name     string
	Priority int
	Source   HookSource
	Trust    HookTrust
	Hook     any
}

func NamedHook(name string, hook any) HookRegistration {
	return HookRegistration{
		Name:   name,
		Source: HookSourceInProcess,
		Trust:  HookTrustTrusted,
		Hook:   hook,
	}
}

// UntrustedNamedHook mounts an in-process observer/narrowing hook. Its
// interceptor return values cannot rewrite requests/results or synthesize a
// response, but deny/abort decisions remain effective.
func UntrustedNamedHook(name string, hook any) HookRegistration {
	return HookRegistration{
		Name:   name,
		Source: HookSourceInProcess,
		Trust:  HookTrustUntrusted,
		Hook:   hook,
	}
}

type RuntimeEventObserver interface {
	OnRuntimeEvent(ctx context.Context, evt runtimeevents.Event) error
}

type LLMInterceptor interface {
	BeforeLLM(ctx context.Context, req *LLMHookRequest) (*LLMHookRequest, HookDecision, error)
	AfterLLM(ctx context.Context, resp *LLMHookResponse) (*LLMHookResponse, HookDecision, error)
}

type ToolInterceptor interface {
	BeforeTool(ctx context.Context, call *ToolCallHookRequest) (*ToolCallHookRequest, HookDecision, error)
	AfterTool(ctx context.Context, result *ToolResultHookResponse) (*ToolResultHookResponse, HookDecision, error)
}

type ToolApprover interface {
	ApproveTool(ctx context.Context, req *ToolApprovalRequest) (ApprovalDecision, error)
}

type LLMHookRequest struct {
	Meta             HookMeta                   `json:"meta"`
	Context          *TurnContext               `json:"context,omitempty"`
	Model            string                     `json:"model"` // Exact configured model alias, never a provider model ID.
	Messages         []providers.Message        `json:"messages,omitempty"`
	Tools            []providers.ToolDefinition `json:"tools,omitempty"`
	Options          map[string]any             `json:"options,omitempty"`
	GracefulTerminal bool                       `json:"graceful_terminal,omitempty"`
}

func (r *LLMHookRequest) Clone() *LLMHookRequest {
	if r == nil {
		return nil
	}
	cloned := *r
	cloned.Meta = cloneHookMeta(r.Meta)
	cloned.Context = cloneTurnContext(r.Context)
	cloned.Messages = cloneProviderMessages(r.Messages)
	cloned.Tools = cloneToolDefinitions(r.Tools)
	cloned.Options = cloneStringAnyMap(r.Options)
	return &cloned
}

type LLMHookResponse struct {
	Meta     HookMeta               `json:"meta"`
	Context  *TurnContext           `json:"context,omitempty"`
	Model    string                 `json:"model"` // Exact configured model alias used for the request.
	Response *providers.LLMResponse `json:"response,omitempty"`

	// toolCallProvenance is manager-owned, index-aligned authority metadata for
	// trusted AfterLLM mutations. It is absent from hook JSON.
	toolCallProvenance []tools.ToolHookProvenance
}

func (r *LLMHookResponse) Clone() *LLMHookResponse {
	if r == nil {
		return nil
	}
	cloned := *r
	cloned.Meta = cloneHookMeta(r.Meta)
	cloned.Context = cloneTurnContext(r.Context)
	cloned.Response = cloneLLMResponse(r.Response)
	cloned.toolCallProvenance = append([]tools.ToolHookProvenance(nil), r.toolCallProvenance...)
	return &cloned
}

func (r *LLMHookResponse) policyToolCallProvenance() []tools.ToolHookProvenance {
	if r == nil || len(r.toolCallProvenance) == 0 {
		return nil
	}
	return append([]tools.ToolHookProvenance(nil), r.toolCallProvenance...)
}

type ToolCallHookRequest struct {
	Meta       HookMeta          `json:"meta"`
	Context    *TurnContext      `json:"context,omitempty"`
	Tool       string            `json:"tool"`
	Arguments  map[string]any    `json:"arguments,omitempty"`
	Channel    string            `json:"channel,omitempty"`
	ChatID     string            `json:"chat_id,omitempty"`
	HookResult *tools.ToolResult `json:"hook_result,omitempty"` // Result returned directly by hook (for respond action). Media is supported - see Media handling section in docs.

	// hookProvenance is manager-owned authority metadata. It is intentionally
	// absent from hook JSON and cannot be supplied by an external hook.
	hookProvenance tools.ToolHookProvenance
}

func (r *ToolCallHookRequest) Clone() *ToolCallHookRequest {
	if r == nil {
		return nil
	}
	cloned := *r
	cloned.Meta = cloneHookMeta(r.Meta)
	cloned.Context = cloneTurnContext(r.Context)
	cloned.Arguments = cloneStringAnyMap(r.Arguments)
	cloned.HookResult = cloneToolResult(r.HookResult)
	return &cloned
}

func (r *ToolCallHookRequest) policyHookProvenance() tools.ToolHookProvenance {
	if r == nil || !r.hookProvenance.Trusted {
		return tools.ToolHookProvenance{}
	}
	return r.hookProvenance
}

type ToolApprovalRequest struct {
	Meta      HookMeta       `json:"meta"`
	Context   *TurnContext   `json:"context,omitempty"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

func (r *ToolApprovalRequest) Clone() *ToolApprovalRequest {
	if r == nil {
		return nil
	}
	cloned := *r
	cloned.Meta = cloneHookMeta(r.Meta)
	cloned.Context = cloneTurnContext(r.Context)
	cloned.Arguments = cloneStringAnyMap(r.Arguments)
	return &cloned
}

type ToolResultHookResponse struct {
	Meta      HookMeta          `json:"meta"`
	Context   *TurnContext      `json:"context,omitempty"`
	Tool      string            `json:"tool"`
	Arguments map[string]any    `json:"arguments,omitempty"`
	Result    *tools.ToolResult `json:"result,omitempty"`
	Duration  time.Duration     `json:"duration"`
}

func (r *ToolResultHookResponse) Clone() *ToolResultHookResponse {
	if r == nil {
		return nil
	}
	cloned := *r
	cloned.Meta = cloneHookMeta(r.Meta)
	cloned.Context = cloneTurnContext(r.Context)
	cloned.Arguments = cloneStringAnyMap(r.Arguments)
	cloned.Result = cloneToolResult(r.Result)
	return &cloned
}

type HookManager struct {
	runtimeEvents      runtimeevents.EventChannel
	timeoutMu          sync.RWMutex
	observerTimeout    time.Duration
	interceptorTimeout time.Duration
	approvalTimeout    time.Duration

	mu       sync.RWMutex
	hooks    map[string]trackedHookRegistration
	ordered  []HookRegistration
	mountSeq uint64

	runtimeSub  runtimeevents.Subscription
	runtimeDone chan struct{}
	closeOnce   sync.Once
	closed      bool
}

type trackedHookRegistration struct {
	registration    HookRegistration
	mountID         uint64
	generationOwned bool
}

func NewHookManager(runtimeEvents runtimeevents.EventChannel) *HookManager {
	hm := &HookManager{
		runtimeEvents:      runtimeEvents,
		observerTimeout:    defaultHookObserverTimeout,
		interceptorTimeout: defaultHookInterceptorTimeout,
		approvalTimeout:    defaultHookApprovalTimeout,
		hooks:              make(map[string]trackedHookRegistration),
		runtimeDone:        make(chan struct{}),
	}

	if runtimeEvents != nil {
		sub, ch, err := runtimeEvents.SubscribeChan(context.Background(), runtimeevents.SubscribeOptions{
			Name:   "hook-manager-observer",
			Buffer: hookObserverBufferSize,
		})
		if err != nil {
			logger.WarnCF("hooks", "Failed to subscribe runtime events for hooks", map[string]any{
				"error": err.Error(),
			})
			close(hm.runtimeDone)
		} else {
			hm.runtimeSub = sub
			go hm.dispatchRuntimeEvents(ch)
		}
	} else {
		close(hm.runtimeDone)
	}

	return hm
}

func (hm *HookManager) Close() {
	if hm == nil {
		return
	}

	hm.closeOnce.Do(func() {
		hm.mu.Lock()
		hm.closed = true
		hm.mu.Unlock()
		if hm.runtimeSub != nil {
			if err := hm.runtimeSub.Close(); err != nil {
				logger.WarnCF("hooks", "Failed to close runtime event hook subscription", map[string]any{
					"error": err.Error(),
				})
			}
		}
		<-hm.runtimeDone
		hm.closeAllHooks()
	})
}

func (hm *HookManager) ConfigureTimeouts(observer, interceptor, approval time.Duration) {
	if hm == nil {
		return
	}
	hm.timeoutMu.Lock()
	defer hm.timeoutMu.Unlock()
	if observer > 0 {
		hm.observerTimeout = observer
	}
	if interceptor > 0 {
		hm.interceptorTimeout = interceptor
	}
	if approval > 0 {
		hm.approvalTimeout = approval
	}
}

func (hm *HookManager) timeoutSnapshot() (time.Duration, time.Duration, time.Duration) {
	if hm == nil {
		return 0, 0, 0
	}
	hm.timeoutMu.RLock()
	defer hm.timeoutMu.RUnlock()
	return hm.observerTimeout, hm.interceptorTimeout, hm.approvalTimeout
}

func (hm *HookManager) Mount(reg HookRegistration) error {
	_, err := hm.mount(reg, false)
	return err
}

func (hm *HookManager) mountTracked(reg HookRegistration) (uint64, error) {
	return hm.mount(reg, true)
}

func (hm *HookManager) mount(
	reg HookRegistration,
	generationOwned bool,
) (uint64, error) {
	if hm == nil {
		return 0, fmt.Errorf("hook manager is nil")
	}
	if reg.Name == "" {
		return 0, fmt.Errorf("hook name is required")
	}
	if reg.Name != strings.TrimSpace(reg.Name) || len(reg.Name) > tools.MaxToolPolicyNameLen {
		return 0, fmt.Errorf("hook name must be exact and bounded")
	}
	for _, character := range reg.Name {
		if character < 0x20 || character == 0x7f {
			return 0, fmt.Errorf("hook name contains control characters")
		}
	}
	if reg.Hook == nil {
		return 0, fmt.Errorf("hook %q is nil", reg.Name)
	}
	if reg.Trust != HookTrustUntrusted && reg.Trust != HookTrustTrusted {
		return 0, fmt.Errorf("hook %q has unsupported trust %d", reg.Name, reg.Trust)
	}

	hm.mu.Lock()
	defer hm.mu.Unlock()
	if hm.closed {
		return 0, fmt.Errorf("hook manager is closed")
	}

	if existing, ok := hm.hooks[reg.Name]; ok {
		if generationOwned && !existing.generationOwned {
			return 0, fmt.Errorf(
				"configured hook %q collides with a manual hook",
				reg.Name,
			)
		}
		closeHookIfPossible(existing.registration.Hook)
	}
	hm.mountSeq++
	mountID := hm.mountSeq
	hm.hooks[reg.Name] = trackedHookRegistration{
		registration:    reg,
		mountID:         mountID,
		generationOwned: generationOwned,
	}
	hm.rebuildOrdered()
	return mountID, nil
}

func hookRegistrationTrusted(reg HookRegistration) bool {
	return reg.Trust == HookTrustTrusted
}

func trustedHookProvenance(reg HookRegistration) tools.ToolHookProvenance {
	if !hookRegistrationTrusted(reg) {
		return tools.ToolHookProvenance{}
	}
	return tools.ToolHookProvenance{
		Name:    reg.Name,
		Source:  reg.Source.String(),
		Trusted: true,
	}
}

func (hm *HookManager) logUntrustedMutation(reg HookRegistration, stage string, action HookAction) {
	logger.WarnCF("hooks", "Discarded mutation from untrusted hook", map[string]any{
		"hook":   reg.Name,
		"source": reg.Source.String(),
		"stage":  stage,
		"action": action,
	})
}

func (hm *HookManager) Unmount(name string) {
	if hm == nil || name == "" {
		return
	}

	hm.mu.Lock()
	defer hm.mu.Unlock()

	if existing, ok := hm.hooks[name]; ok {
		closeHookIfPossible(existing.registration.Hook)
	}
	delete(hm.hooks, name)
	hm.rebuildOrdered()
}

func (hm *HookManager) unmountTracked(name string, mountID uint64) {
	if hm == nil || name == "" || mountID == 0 {
		return
	}
	hm.mu.Lock()
	defer hm.mu.Unlock()
	existing, ok := hm.hooks[name]
	if !ok || existing.mountID != mountID {
		return
	}
	closeHookIfPossible(existing.registration.Hook)
	delete(hm.hooks, name)
	hm.rebuildOrdered()
}

func (hm *HookManager) dispatchRuntimeEvents(ch <-chan runtimeevents.Event) {
	defer close(hm.runtimeDone)

	for evt := range ch {
		for _, reg := range hm.snapshotHooks() {
			observer, ok := reg.Hook.(RuntimeEventObserver)
			if !ok {
				continue
			}
			hm.runRuntimeObserver(reg.Name, observer, evt)
		}
	}
}

func (hm *HookManager) BeforeLLM(ctx context.Context, req *LLMHookRequest) (*LLMHookRequest, HookDecision) {
	if hm == nil || req == nil {
		return req, HookDecision{Action: HookActionContinue}
	}

	current, err := detachLLMHookRequest(req)
	if err != nil {
		logger.WarnCF("hooks", "Skipping BeforeLLM hooks for invalid detached request", nil)
		return req.Clone(), HookDecision{Action: HookActionContinue}
	}
	for _, reg := range hm.snapshotHooks() {
		interceptor, ok := reg.Hook.(LLMInterceptor)
		if !ok {
			continue
		}

		hookInput, cloneErr := detachLLMHookRequest(current)
		if cloneErr != nil {
			logger.WarnCF(
				"hooks",
				"Skipping BeforeLLM hook for invalid detached request",
				map[string]any{"hook": reg.Name},
			)
			continue
		}
		next, decision, ok := hm.callBeforeLLM(ctx, reg.Name, interceptor, hookInput)
		if !ok {
			continue
		}

		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if !hookRegistrationTrusted(reg) {
				if decision.normalizedAction() == HookActionModify {
					hm.logUntrustedMutation(reg, "before_llm", decision.normalizedAction())
				}
				continue
			}
			if next != nil {
				detached, detachErr := detachLLMHookRequest(next)
				if detachErr != nil {
					logger.WarnCF(
						"hooks",
						"Discarded invalid BeforeLLM hook mutation",
						map[string]any{"hook": reg.Name},
					)
					continue
				}
				detached = hm.applyBeforeLLMControls(reg.Name, current, detached)
				current = detached
			}
		case HookActionAbortTurn, HookActionHardAbort:
			return current, decision
		case HookActionRespond:
			if !hookRegistrationTrusted(reg) {
				hm.logUntrustedMutation(reg, "before_llm", HookActionRespond)
				continue
			}
			hm.logUnsupportedAction(reg.Name, "before_llm", decision.Action)
		default:
			hm.logUnsupportedAction(reg.Name, "before_llm", decision.Action)
		}
	}
	return current, HookDecision{Action: HookActionContinue}
}

func (hm *HookManager) AfterLLM(ctx context.Context, resp *LLMHookResponse) (*LLMHookResponse, HookDecision) {
	if hm == nil || resp == nil {
		return resp, HookDecision{Action: HookActionContinue}
	}

	current, err := detachLLMHookResponse(resp)
	if err != nil {
		logger.WarnCF("hooks", "Skipping AfterLLM hooks for invalid detached response", nil)
		return resp.Clone(), HookDecision{Action: HookActionContinue}
	}
	for _, reg := range hm.snapshotHooks() {
		interceptor, ok := reg.Hook.(LLMInterceptor)
		if !ok {
			continue
		}

		hookInput, cloneErr := detachLLMHookResponse(current)
		if cloneErr != nil {
			logger.WarnCF(
				"hooks",
				"Skipping AfterLLM hook for invalid detached response",
				map[string]any{"hook": reg.Name},
			)
			continue
		}
		next, decision, ok := hm.callAfterLLM(ctx, reg.Name, interceptor, hookInput)
		if !ok {
			continue
		}

		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if !hookRegistrationTrusted(reg) {
				if decision.normalizedAction() == HookActionModify {
					hm.logUntrustedMutation(reg, "after_llm", decision.normalizedAction())
				}
				continue
			}
			if next != nil {
				detached, detachErr := detachLLMHookResponse(next)
				if detachErr != nil {
					logger.WarnCF("hooks", "Discarded invalid AfterLLM hook mutation", map[string]any{"hook": reg.Name})
					continue
				}
				detached.toolCallProvenance = afterLLMToolCallProvenance(reg, current, detached)
				current = detached
			}
		case HookActionAbortTurn, HookActionHardAbort:
			return current, decision
		case HookActionRespond:
			if !hookRegistrationTrusted(reg) {
				hm.logUntrustedMutation(reg, "after_llm", HookActionRespond)
				continue
			}
			hm.logUnsupportedAction(reg.Name, "after_llm", decision.Action)
		default:
			hm.logUnsupportedAction(reg.Name, "after_llm", decision.Action)
		}
	}
	return current, HookDecision{Action: HookActionContinue}
}

func afterLLMToolCallProvenance(
	reg HookRegistration,
	current, next *LLMHookResponse,
) []tools.ToolHookProvenance {
	if next == nil || next.Response == nil || len(next.Response.ToolCalls) == 0 {
		return nil
	}
	provenance := make([]tools.ToolHookProvenance, len(next.Response.ToolCalls))
	var currentCalls []providers.ToolCall
	if current != nil && current.Response != nil {
		currentCalls = current.Response.ToolCalls
	}
	for index := range next.Response.ToolCalls {
		if index < len(currentCalls) && reflect.DeepEqual(
			currentCalls[index],
			next.Response.ToolCalls[index],
		) {
			if index < len(current.toolCallProvenance) {
				provenance[index] = current.toolCallProvenance[index]
			}
			continue
		}
		provenance[index] = trustedHookProvenance(reg)
	}
	return provenance
}

func (hm *HookManager) applyBeforeLLMControls(
	hookName string,
	current *LLMHookRequest,
	next *LLMHookRequest,
) *LLMHookRequest {
	if next == nil || current == nil {
		return next
	}
	if !llmHookSystemMessagesUnchanged(current.Messages, next.Messages) {
		logger.WarnCF("hooks", "Hook attempted to modify system prompt; preserving original messages", map[string]any{
			"hook": hookName,
		})
		next.Messages = cloneProviderMessages(current.Messages)
	}
	if !llmHookToolDefinitionsUnchanged(current.Tools, next.Tools) {
		logger.WarnCF("hooks", "Hook attempted to modify tool definitions; preserving original tools", map[string]any{
			"hook": hookName,
		})
		next.Tools = cloneToolDefinitions(current.Tools)
	}
	return next
}

func llmHookSystemMessagesUnchanged(before, after []providers.Message) bool {
	beforeSystem := systemMessageFingerprints(before)
	afterSystem := systemMessageFingerprints(after)
	return reflect.DeepEqual(beforeSystem, afterSystem)
}

type systemMessageFingerprint struct {
	Index   int
	Message providers.Message
}

func systemMessageFingerprints(messages []providers.Message) []systemMessageFingerprint {
	var fingerprints []systemMessageFingerprint
	for i, msg := range messages {
		if msg.Role != "system" {
			continue
		}
		msg = providerVisibleMessage(msg)
		fingerprints = append(fingerprints, systemMessageFingerprint{
			Index:   i,
			Message: cloneProviderMessages([]providers.Message{msg})[0],
		})
	}
	return fingerprints
}

func llmHookToolDefinitionsUnchanged(before, after []providers.ToolDefinition) bool {
	return reflect.DeepEqual(providerVisibleToolDefinitions(before), providerVisibleToolDefinitions(after))
}

func providerVisibleMessage(msg providers.Message) providers.Message {
	msg.PromptLayer = ""
	msg.PromptSlot = ""
	msg.PromptSource = ""
	if len(msg.SystemParts) > 0 {
		msg.SystemParts = append([]providers.ContentBlock(nil), msg.SystemParts...)
		for i := range msg.SystemParts {
			msg.SystemParts[i].PromptLayer = ""
			msg.SystemParts[i].PromptSlot = ""
			msg.SystemParts[i].PromptSource = ""
		}
	}
	return msg
}

func providerVisibleToolDefinitions(defs []providers.ToolDefinition) []providers.ToolDefinition {
	cloned := cloneToolDefinitions(defs)
	for i := range cloned {
		cloned[i].PromptLayer = ""
		cloned[i].PromptSlot = ""
		cloned[i].PromptSource = ""
	}
	return cloned
}

func (hm *HookManager) BeforeTool(
	ctx context.Context,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision) {
	if hm == nil || call == nil {
		return call, HookDecision{Action: HookActionContinue}
	}

	current, err := detachToolCallHookRequest(call)
	if err != nil {
		return invalidToolCallHookRequest(call), HookDecision{
			Action: HookActionDenyTool,
			Reason: "tool arguments are invalid",
		}
	}
	for _, reg := range hm.snapshotHooks() {
		interceptor, ok := reg.Hook.(ToolInterceptor)
		if !ok {
			continue
		}

		hookInput, cloneErr := detachToolCallHookRequest(current)
		if cloneErr != nil {
			return invalidToolCallHookRequest(current), HookDecision{
				Action: HookActionDenyTool,
				Reason: "tool arguments are invalid",
			}
		}
		next, decision, ok := hm.callBeforeTool(ctx, reg.Name, interceptor, hookInput)
		if !ok {
			continue
		}

		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if !hookRegistrationTrusted(reg) {
				if decision.normalizedAction() == HookActionModify {
					hm.logUntrustedMutation(reg, "before_tool", decision.normalizedAction())
				}
				continue
			}
			if next != nil {
				detached, detachErr := detachToolCallHookRequest(next)
				if detachErr != nil {
					return invalidToolCallHookRequest(current), HookDecision{
						Action: HookActionDenyTool,
						Reason: "trusted hook returned invalid tool arguments",
					}
				}
				previousProvenance := current.hookProvenance
				detached.hookProvenance = previousProvenance
				if decision.normalizedAction() == HookActionModify ||
					detached.Tool != current.Tool ||
					!reflect.DeepEqual(detached.Arguments, current.Arguments) {
					detached.hookProvenance = trustedHookProvenance(reg)
				}
				current = detached
			}
		case HookActionRespond:
			if !hookRegistrationTrusted(reg) {
				hm.logUntrustedMutation(reg, "before_tool", HookActionRespond)
				continue
			}
			if next == nil {
				return nil, decision
			}
			detached, detachErr := detachToolCallHookRequest(next)
			if detachErr != nil {
				return nil, decision
			}
			detached.hookProvenance = trustedHookProvenance(reg)
			return detached, decision
		case HookActionDenyTool, HookActionAbortTurn, HookActionHardAbort:
			return current, decision
		default:
			hm.logUnsupportedAction(reg.Name, "before_tool", decision.Action)
		}
	}
	return current, HookDecision{Action: HookActionContinue}
}

func (hm *HookManager) AfterTool(
	ctx context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision) {
	if hm == nil || result == nil {
		return result, HookDecision{Action: HookActionContinue}
	}

	current, err := detachToolResultHookResponse(result)
	if err != nil {
		logger.WarnCF("hooks", "Skipping AfterTool hooks for invalid detached result", nil)
		return result.Clone(), HookDecision{Action: HookActionContinue}
	}
	for _, reg := range hm.snapshotHooks() {
		interceptor, ok := reg.Hook.(ToolInterceptor)
		if !ok {
			continue
		}

		hookInput, cloneErr := detachToolResultHookResponse(current)
		if cloneErr != nil {
			logger.WarnCF(
				"hooks",
				"Skipping AfterTool hook for invalid detached result",
				map[string]any{"hook": reg.Name},
			)
			continue
		}
		next, decision, ok := hm.callAfterTool(ctx, reg.Name, interceptor, hookInput)
		if !ok {
			continue
		}

		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if !hookRegistrationTrusted(reg) {
				if decision.normalizedAction() == HookActionModify {
					hm.logUntrustedMutation(reg, "after_tool", decision.normalizedAction())
				}
				continue
			}
			if next != nil {
				// Tool and Arguments identify the already-authorized effect. Even a
				// trusted result hook may change only the result projection.
				nextCopy := *next
				nextCopy.Tool = current.Tool
				nextCopy.Arguments = current.Arguments
				detached, detachErr := detachToolResultHookResponse(&nextCopy)
				if detachErr != nil {
					logger.WarnCF(
						"hooks",
						"Discarded invalid AfterTool hook mutation",
						map[string]any{"hook": reg.Name},
					)
					continue
				}
				current = detached
			}
		case HookActionAbortTurn, HookActionHardAbort:
			return current, decision
		case HookActionRespond:
			if !hookRegistrationTrusted(reg) {
				hm.logUntrustedMutation(reg, "after_tool", HookActionRespond)
				continue
			}
			hm.logUnsupportedAction(reg.Name, "after_tool", decision.Action)
		default:
			hm.logUnsupportedAction(reg.Name, "after_tool", decision.Action)
		}
	}
	return current, HookDecision{Action: HookActionContinue}
}

func (hm *HookManager) ApproveTool(ctx context.Context, req *ToolApprovalRequest) ApprovalDecision {
	if hm == nil || req == nil {
		return ApprovalDecision{Approved: true}
	}

	for _, reg := range hm.snapshotHooks() {
		approver, ok := reg.Hook.(ToolApprover)
		if !ok {
			continue
		}

		detached, detachErr := detachToolApprovalRequest(req)
		if detachErr != nil {
			return ApprovalDecision{Approved: false, Reason: "tool arguments are invalid"}
		}
		decision, ok := hm.callApproveTool(ctx, reg.Name, approver, detached)
		if !ok {
			return ApprovalDecision{
				Approved: false,
				Reason:   fmt.Sprintf("tool approval hook %q failed", reg.Name),
			}
		}
		if !decision.Approved {
			return decision
		}
	}

	return ApprovalDecision{Approved: true}
}

func (hm *HookManager) rebuildOrdered() {
	hm.ordered = hm.ordered[:0]
	for _, reg := range hm.hooks {
		hm.ordered = append(hm.ordered, reg.registration)
	}
	sort.SliceStable(hm.ordered, func(i, j int) bool {
		if hm.ordered[i].Source != hm.ordered[j].Source {
			return hm.ordered[i].Source < hm.ordered[j].Source
		}
		if hm.ordered[i].Priority == hm.ordered[j].Priority {
			return hm.ordered[i].Name < hm.ordered[j].Name
		}
		return hm.ordered[i].Priority < hm.ordered[j].Priority
	})
}

func (hm *HookManager) snapshotHooks() []HookRegistration {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	snapshot := make([]HookRegistration, len(hm.ordered))
	copy(snapshot, hm.ordered)
	return snapshot
}

func (hm *HookManager) closeAllHooks() {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	for name, reg := range hm.hooks {
		closeHookIfPossible(reg.registration.Hook)
		delete(hm.hooks, name)
	}
	hm.ordered = nil
}

func (hm *HookManager) runRuntimeObserver(
	name string,
	observer RuntimeEventObserver,
	evt runtimeevents.Event,
) {
	observerTimeout, _, _ := hm.timeoutSnapshot()
	ctx, cancel := context.WithTimeout(context.Background(), observerTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- observer.OnRuntimeEvent(ctx, evt)
	}()

	select {
	case err := <-done:
		if err != nil {
			logger.WarnCF("hooks", "Runtime event observer failed", map[string]any{
				"hook":  name,
				"event": evt.Kind.String(),
				"error": err.Error(),
			})
		}
	case <-ctx.Done():
		logger.WarnCF("hooks", "Runtime event observer timed out", map[string]any{
			"hook":       name,
			"event":      evt.Kind.String(),
			"timeout_ms": observerTimeout.Milliseconds(),
		})
	}
}

func (hm *HookManager) callBeforeLLM(
	parent context.Context,
	name string,
	interceptor LLMInterceptor,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, bool) {
	_, interceptorTimeout, _ := hm.timeoutSnapshot()
	return runInterceptorHook(
		parent,
		interceptorTimeout,
		name,
		"before_llm",
		func(ctx context.Context) (*LLMHookRequest, HookDecision, error) {
			return interceptor.BeforeLLM(ctx, req)
		},
	)
}

func (hm *HookManager) callAfterLLM(
	parent context.Context,
	name string,
	interceptor LLMInterceptor,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, bool) {
	_, interceptorTimeout, _ := hm.timeoutSnapshot()
	return runInterceptorHook(
		parent,
		interceptorTimeout,
		name,
		"after_llm",
		func(ctx context.Context) (*LLMHookResponse, HookDecision, error) {
			return interceptor.AfterLLM(ctx, resp)
		},
	)
}

func (hm *HookManager) callBeforeTool(
	parent context.Context,
	name string,
	interceptor ToolInterceptor,
	call *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, bool) {
	_, interceptorTimeout, _ := hm.timeoutSnapshot()
	return runInterceptorHook(
		parent,
		interceptorTimeout,
		name,
		"before_tool",
		func(ctx context.Context) (*ToolCallHookRequest, HookDecision, error) {
			return interceptor.BeforeTool(ctx, call)
		},
	)
}

func (hm *HookManager) callAfterTool(
	parent context.Context,
	name string,
	interceptor ToolInterceptor,
	resultView *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, bool) {
	_, interceptorTimeout, _ := hm.timeoutSnapshot()
	return runInterceptorHook(
		parent,
		interceptorTimeout,
		name,
		"after_tool",
		func(ctx context.Context) (*ToolResultHookResponse, HookDecision, error) {
			return interceptor.AfterTool(ctx, resultView)
		},
	)
}

func (hm *HookManager) callApproveTool(
	parent context.Context,
	name string,
	approver ToolApprover,
	req *ToolApprovalRequest,
) (ApprovalDecision, bool) {
	_, _, approvalTimeout := hm.timeoutSnapshot()
	return runApprovalHook(
		parent,
		approvalTimeout,
		name,
		"approve_tool",
		func(ctx context.Context) (ApprovalDecision, error) {
			return approver.ApproveTool(ctx, req)
		},
	)
}

func runInterceptorHook[T any](
	parent context.Context,
	timeout time.Duration,
	name string,
	stage string,
	fn func(ctx context.Context) (T, HookDecision, error),
) (T, HookDecision, bool) {
	var zero T

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	type result struct {
		value    T
		decision HookDecision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		value, decision, err := fn(ctx)
		done <- result{value: value, decision: decision, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			logger.WarnCF("hooks", "Interceptor hook failed", map[string]any{
				"hook":  name,
				"stage": stage,
				"error": res.err.Error(),
			})
			return zero, HookDecision{}, false
		}
		return res.value, res.decision, true
	case <-ctx.Done():
		logger.WarnCF("hooks", "Interceptor hook timed out", map[string]any{
			"hook":       name,
			"stage":      stage,
			"timeout_ms": timeout.Milliseconds(),
		})
		return zero, HookDecision{}, false
	}
}

func runApprovalHook(
	parent context.Context,
	timeout time.Duration,
	name string,
	stage string,
	fn func(ctx context.Context) (ApprovalDecision, error),
) (ApprovalDecision, bool) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	type result struct {
		decision ApprovalDecision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		decision, err := fn(ctx)
		done <- result{decision: decision, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			logger.WarnCF("hooks", "Approval hook failed", map[string]any{
				"hook":  name,
				"stage": stage,
				"error": res.err.Error(),
			})
			return ApprovalDecision{}, false
		}
		return res.decision, true
	case <-ctx.Done():
		logger.WarnCF("hooks", "Approval hook timed out", map[string]any{
			"hook":       name,
			"stage":      stage,
			"timeout_ms": timeout.Milliseconds(),
		})
		return ApprovalDecision{
			Approved: false,
			Reason:   fmt.Sprintf("tool approval hook %q timed out", name),
		}, true
	}
}

func (hm *HookManager) logUnsupportedAction(name, stage string, action HookAction) {
	logger.WarnCF("hooks", "Hook returned unsupported action for stage", map[string]any{
		"hook":   name,
		"stage":  stage,
		"action": action,
	})
}

func validateDetachedMap(values map[string]any) error {
	_, err := tools.DetachToolArguments(values)
	return err
}

func validateProviderToolCallsDetached(calls []providers.ToolCall) error {
	for index := range calls {
		if err := validateDetachedMap(calls[index].Arguments); err != nil {
			return fmt.Errorf("tool call %d arguments: %w", index+1, err)
		}
	}
	return nil
}

func validateProviderMessagesDetached(messages []providers.Message) error {
	for index := range messages {
		if err := validateProviderToolCallsDetached(messages[index].ToolCalls); err != nil {
			return fmt.Errorf("message %d: %w", index+1, err)
		}
	}
	return nil
}

func validateToolDefinitionsDetached(definitions []providers.ToolDefinition) error {
	for index := range definitions {
		if err := validateDetachedMap(definitions[index].Function.Parameters); err != nil {
			return fmt.Errorf("tool definition %d parameters: %w", index+1, err)
		}
	}
	return nil
}

func validateToolResultDetached(result *tools.ToolResult) error {
	if result == nil {
		return nil
	}
	return validateProviderMessagesDetached(result.Messages)
}

func detachLLMHookRequest(request *LLMHookRequest) (*LLMHookRequest, error) {
	if request == nil {
		return nil, nil
	}
	if err := validateProviderMessagesDetached(request.Messages); err != nil {
		return nil, err
	}
	if err := validateToolDefinitionsDetached(request.Tools); err != nil {
		return nil, err
	}
	if err := validateDetachedMap(request.Options); err != nil {
		return nil, err
	}
	return request.Clone(), nil
}

func detachLLMHookResponse(response *LLMHookResponse) (*LLMHookResponse, error) {
	if response == nil {
		return nil, nil
	}
	if response.Response != nil {
		if err := validateProviderToolCallsDetached(response.Response.ToolCalls); err != nil {
			return nil, err
		}
	}
	cloned := response.Clone()
	if cloned != nil && cloned.Response != nil {
		for index := range cloned.Response.ToolCalls {
			arguments, err := tools.DetachToolArguments(cloned.Response.ToolCalls[index].Arguments)
			if err != nil {
				return nil, err
			}
			cloned.Response.ToolCalls[index].Arguments = arguments
		}
	}
	return cloned, nil
}

func detachToolCallHookRequest(request *ToolCallHookRequest) (*ToolCallHookRequest, error) {
	if request == nil {
		return nil, nil
	}
	if err := validateDetachedMap(request.Arguments); err != nil {
		return nil, err
	}
	if err := validateToolResultDetached(request.HookResult); err != nil {
		return nil, err
	}
	cloned := request.Clone()
	arguments, err := tools.DetachToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	cloned.Arguments = arguments
	return cloned, nil
}

func invalidToolCallHookRequest(request *ToolCallHookRequest) *ToolCallHookRequest {
	if request == nil {
		return nil
	}
	cloned := *request
	cloned.Meta = cloneHookMeta(request.Meta)
	cloned.Context = cloneTurnContext(request.Context)
	cloned.Arguments = map[string]any{}
	cloned.HookResult = nil
	cloned.hookProvenance = tools.ToolHookProvenance{}
	return &cloned
}

func detachToolApprovalRequest(request *ToolApprovalRequest) (*ToolApprovalRequest, error) {
	if request == nil {
		return nil, nil
	}
	if err := validateDetachedMap(request.Arguments); err != nil {
		return nil, err
	}
	cloned := request.Clone()
	arguments, err := tools.DetachToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	cloned.Arguments = arguments
	return cloned, nil
}

func detachToolResultHookResponse(response *ToolResultHookResponse) (*ToolResultHookResponse, error) {
	if response == nil {
		return nil, nil
	}
	if err := validateDetachedMap(response.Arguments); err != nil {
		return nil, err
	}
	if err := validateToolResultDetached(response.Result); err != nil {
		return nil, err
	}
	cloned := response.Clone()
	arguments, err := tools.DetachToolArguments(response.Arguments)
	if err != nil {
		return nil, err
	}
	cloned.Arguments = arguments
	return cloned, nil
}

func cloneProviderMessages(messages []providers.Message) []providers.Message {
	if len(messages) == 0 {
		return nil
	}

	cloned := make([]providers.Message, len(messages))
	for i, msg := range messages {
		cloned[i] = msg
		if msg.CreatedAt != nil {
			createdAt := *msg.CreatedAt
			cloned[i].CreatedAt = &createdAt
		}
		if len(msg.Media) > 0 {
			cloned[i].Media = append([]string(nil), msg.Media...)
		}
		if len(msg.Attachments) > 0 {
			cloned[i].Attachments = append([]providers.Attachment(nil), msg.Attachments...)
		}
		if len(msg.Parts) > 0 {
			cloned[i].Parts = append([]providers.PromptPart(nil), msg.Parts...)
		}
		if len(msg.SystemParts) > 0 {
			cloned[i].SystemParts = append([]providers.ContentBlock(nil), msg.SystemParts...)
			for index := range cloned[i].SystemParts {
				if msg.SystemParts[index].CacheControl != nil {
					cacheControl := *msg.SystemParts[index].CacheControl
					cloned[i].SystemParts[index].CacheControl = &cacheControl
				}
			}
		}
		if len(msg.ToolCalls) > 0 {
			cloned[i].ToolCalls = cloneProviderToolCalls(msg.ToolCalls)
		}
	}
	return cloned
}

func cloneProviderToolCalls(calls []providers.ToolCall) []providers.ToolCall {
	if len(calls) == 0 {
		return nil
	}

	cloned := make([]providers.ToolCall, len(calls))
	for i, call := range calls {
		cloned[i] = call
		if call.Function != nil {
			fn := *call.Function
			cloned[i].Function = &fn
		}
		if call.Arguments != nil {
			cloned[i].Arguments = cloneStringAnyMap(call.Arguments)
		}
		if call.ExtraContent != nil {
			extra := *call.ExtraContent
			if call.ExtraContent.Google != nil {
				google := *call.ExtraContent.Google
				extra.Google = &google
			}
			cloned[i].ExtraContent = &extra
		}
	}
	return cloned
}

func cloneToolDefinitions(defs []providers.ToolDefinition) []providers.ToolDefinition {
	if len(defs) == 0 {
		return nil
	}

	cloned := make([]providers.ToolDefinition, len(defs))
	for i, def := range defs {
		cloned[i] = def
		cloned[i].Function.Parameters = cloneStringAnyMap(def.Function.Parameters)
	}
	return cloned
}

func cloneLLMResponse(resp *providers.LLMResponse) *providers.LLMResponse {
	if resp == nil {
		return nil
	}
	cloned := *resp
	cloned.ToolCalls = cloneProviderToolCalls(resp.ToolCalls)
	if len(resp.ReasoningDetails) > 0 {
		cloned.ReasoningDetails = append(cloned.ReasoningDetails[:0:0], resp.ReasoningDetails...)
	}
	if resp.Usage != nil {
		usage := *resp.Usage
		cloned.Usage = &usage
	}
	return &cloned
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if cloned, err := tools.DetachToolArguments(src); err == nil {
		return cloned
	}

	// Public Clone helpers cannot return an error. HookManager validates and
	// detaches before invoking or accepting a hook, so this compatibility
	// fallback is unreachable at an authority boundary.
	cloned := make(map[string]any, len(src))
	for k, v := range src {
		cloned[k] = v
	}
	return cloned
}

func cloneToolResult(result *tools.ToolResult) *tools.ToolResult {
	if result == nil {
		return nil
	}

	cloned := *result
	if len(result.Media) > 0 {
		cloned.Media = append([]string(nil), result.Media...)
	}
	if len(result.ArtifactTags) > 0 {
		cloned.ArtifactTags = append([]string(nil), result.ArtifactTags...)
	}
	if len(result.Messages) > 0 {
		cloned.Messages = cloneProviderMessages(result.Messages)
	}
	return &cloned
}

func closeHookIfPossible(hook any) {
	closer, ok := hook.(io.Closer)
	if !ok {
		return
	}
	if err := closer.Close(); err != nil {
		logger.WarnCF("hooks", "Failed to close hook", map[string]any{
			"error": err.Error(),
		})
	}
}
