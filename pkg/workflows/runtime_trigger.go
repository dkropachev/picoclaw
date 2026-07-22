package workflows

import (
	"fmt"
	"strings"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

type RuntimeEventMatch struct {
	Inputs   map[string]any
	Event    map[string]any
	Session  string
	Delivery Delivery
}

func MatchRuntimeEvent(workflow *Workflow, ref string, evt runtimeevents.Event) (*RuntimeEventMatch, bool, error) {
	if workflow == nil || workflow.On.RuntimeEvent == nil {
		return nil, false, nil
	}
	if isWorkflowTriggerFeedbackEvent(ref, evt) {
		return nil, false, nil
	}
	trigger := workflow.On.RuntimeEvent
	if !stringListMatches(trigger.Kinds, evt.Kind.String(), true) {
		return nil, false, nil
	}
	if !sourceMatches(trigger.Sources, evt.Source) {
		return nil, false, nil
	}
	if !stringListMatches(trigger.Agents, evt.Scope.AgentID, false) {
		return nil, false, nil
	}
	if !stringListMatches(trigger.Sessions, evt.Scope.SessionKey, false) {
		return nil, false, nil
	}
	if !stringListMatches(trigger.Channels, evt.Scope.Channel, true) {
		return nil, false, nil
	}
	if !stringListMatches(trigger.Chats, evt.Scope.ChatID, false) {
		return nil, false, nil
	}
	eventMap := RuntimeEventToMap(evt)
	return &RuntimeEventMatch{
		Inputs: map[string]any{
			"kind":  evt.Kind.String(),
			"event": eventMap,
		},
		Event:    eventMap,
		Session:  RuntimeEventSessionKey(ref, evt),
		Delivery: RuntimeEventDelivery(evt),
	}, true, nil
}

func isWorkflowTriggerFeedbackEvent(ref string, evt runtimeevents.Event) bool {
	if !strings.EqualFold(strings.TrimSpace(evt.Source.Component), "workflow") {
		return false
	}
	if evt.Kind == runtimeevents.KindWorkflowTriggered {
		return true
	}
	if !workflowLifecycleEventKind(evt.Kind) {
		return false
	}
	sourceName := strings.TrimSpace(evt.Source.Name)
	if sourceName == "" {
		if payload, ok := evt.Payload.(map[string]any); ok {
			sourceName, _ = payload["workflow_ref"].(string)
			sourceName = strings.TrimSpace(sourceName)
		}
	}
	return sourceName != "" && strings.EqualFold(sourceName, strings.TrimSpace(ref))
}

func workflowLifecycleEventKind(kind runtimeevents.Kind) bool {
	value := kind.String()
	return strings.HasPrefix(value, "workflow.run.") ||
		strings.HasPrefix(value, "workflow.job.") ||
		strings.HasPrefix(value, "workflow.step.")
}

func RuntimeEventToMap(evt runtimeevents.Event) map[string]any {
	return map[string]any{
		"id":          evt.ID,
		"kind":        evt.Kind.String(),
		"time":        evt.Time,
		"source":      runtimeSourceMap(evt.Source),
		"scope":       runtimeScopeMap(evt.Scope),
		"severity":    string(evt.Severity),
		"payload":     evt.Payload,
		"attrs":       cloneMap(evt.Attrs),
		"trace_id":    evt.Correlation.TraceID,
		"request_id":  evt.Correlation.RequestID,
		"reply_to_id": evt.Correlation.ReplyToID,
	}
}

func RuntimeEventSessionKey(ref string, evt runtimeevents.Event) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "workflow"
	}
	if evt.Scope.SessionKey != "" {
		return fmt.Sprintf("workflow:%s:runtime:%s", ref, evt.Scope.SessionKey)
	}
	kind := strings.TrimSpace(evt.Kind.String())
	if kind == "" {
		kind = "event"
	}
	return fmt.Sprintf("workflow:%s:runtime:%s", ref, kind)
}

func RuntimeEventDelivery(evt runtimeevents.Event) Delivery {
	if strings.TrimSpace(evt.Scope.Channel) == "" || strings.TrimSpace(evt.Scope.ChatID) == "" {
		return Delivery{}
	}
	replyTo := strings.TrimSpace(evt.Correlation.ReplyToID)
	if replyTo == "" {
		replyTo = strings.TrimSpace(evt.Scope.MessageID)
	}
	return Delivery{
		Channel:          evt.Scope.Channel,
		ChatID:           evt.Scope.ChatID,
		TopicID:          evt.Scope.TopicID,
		ThreadTS:         evt.Scope.TopicID,
		MessageID:        evt.Scope.MessageID,
		ReplyToMessageID: replyTo,
	}
}

func sourceMatches(values []string, source runtimeevents.Source) bool {
	if len(values) == 0 {
		return true
	}
	component := strings.TrimSpace(source.Component)
	full := component
	if strings.TrimSpace(source.Name) != "" {
		full += "/" + strings.TrimSpace(source.Name)
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.EqualFold(value, component) || strings.EqualFold(value, full) {
			return true
		}
	}
	return false
}

func runtimeSourceMap(source runtimeevents.Source) map[string]any {
	return map[string]any{
		"component": source.Component,
		"name":      source.Name,
	}
}

func runtimeScopeMap(scope runtimeevents.Scope) map[string]any {
	return map[string]any{
		"runtime_id":  scope.RuntimeID,
		"agent_id":    scope.AgentID,
		"session_key": scope.SessionKey,
		"turn_id":     scope.TurnID,
		"channel":     scope.Channel,
		"account":     scope.Account,
		"chat_id":     scope.ChatID,
		"topic_id":    scope.TopicID,
		"space_id":    scope.SpaceID,
		"space_type":  scope.SpaceType,
		"chat_type":   scope.ChatType,
		"sender_id":   scope.SenderID,
		"message_id":  scope.MessageID,
	}
}
