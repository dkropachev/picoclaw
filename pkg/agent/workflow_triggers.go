package agent

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func (al *AgentLoop) handleWorkflowTriggers(ctx context.Context, msg bus.InboundMessage) bool {
	if al == nil || al.cfg == nil || !al.cfg.Workflows.Enabled {
		return false
	}
	msg = bus.NormalizeInboundMessage(msg)
	if msg.Channel == "" || constants.IsInternalChannel(msg.Channel) {
		return false
	}
	workspace := al.cfg.WorkspacePath()
	defs, err := workflows.ListLocal(ctx, workspace)
	if err != nil {
		logger.WarnCF("workflow", "Failed to list workflows", map[string]any{"error": err.Error()})
		return false
	}
	if len(defs) == 0 {
		return false
	}
	defaultAgent := al.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil {
		return false
	}
	consume := false
	event := workflowEventFromInbound(msg)
	for _, def := range defs {
		if def.Error != "" {
			continue
		}
		workflow, err := workflows.LoadLocal(ctx, workspace, def.Ref)
		if err != nil {
			logger.WarnCF("workflow", "Failed to load workflow", map[string]any{"ref": def.Ref, "error": err.Error()})
			continue
		}
		if err := workflows.Validate(workflow); err != nil {
			logger.WarnCF("workflow", "Invalid workflow skipped", map[string]any{"ref": def.Ref, "error": err.Error()})
			continue
		}
		match, ok, err := workflows.MatchCommandMessage(workflow, def.Ref, event)
		if err != nil {
			logger.WarnCF("workflow", "Workflow command trigger evaluation failed", map[string]any{"ref": def.Ref, "error": err.Error()})
			continue
		}
		if !ok {
			match, ok, err = workflows.MatchChannelMessage(workflow, def.Ref, event)
		}
		if err != nil {
			logger.WarnCF("workflow", "Workflow trigger evaluation failed", map[string]any{"ref": def.Ref, "error": err.Error()})
			continue
		}
		if !ok {
			continue
		}
		if !match.Passthrough {
			consume = true
		}
		al.publishWorkflowTriggered(def.Ref, msg, match)
		executor := al.newWorkflowExecutor(workspace, defaultAgent)
		go func(ref string, m *workflows.ChannelMessageMatch) {
			if _, err := executor.Run(ctx, workflows.RunRequest{
				Ref:      ref,
				Inputs:   m.Inputs,
				Event:    m.Event,
				Session:  m.Session,
				Delivery: m.Delivery,
			}); err != nil {
				logger.WarnCF("workflow", "Workflow run failed", map[string]any{"ref": ref, "error": err.Error()})
			}
		}(def.Ref, match)
	}
	return consume
}

func (al *AgentLoop) newWorkflowExecutor(workspace string, defaultAgent *AgentInstance) *workflows.Executor {
	executor := &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        workflows.NewFileRunStore(workspace),
		Agents:       &workflowAgentRunner{loop: al},
	}
	if al != nil && al.cfg != nil {
		executor.MaxCallDepth = al.cfg.Workflows.EffectiveMaxCallDepth()
		executor.MaxConcurrentRuns = al.cfg.Workflows.EffectiveMaxConcurrentRuns()
		executor.DefaultTimeout = al.cfg.Workflows.EffectiveDefaultTimeout()
	}
	if defaultAgent != nil {
		executor.Tools = &workflowToolRunner{
			agentID:  defaultAgent.ID,
			registry: defaultAgent.Tools,
			loop:     al,
		}
	}
	return executor
}

func workflowEventFromInbound(msg bus.InboundMessage) workflows.ChannelMessageEvent {
	msg = bus.NormalizeInboundMessage(msg)
	return workflows.ChannelMessageEvent{
		Channel:          msg.Context.Channel,
		Account:          msg.Context.Account,
		ChatID:           msg.Context.ChatID,
		ChatType:         msg.Context.ChatType,
		TopicID:          msg.Context.TopicID,
		SpaceID:          msg.Context.SpaceID,
		SpaceType:        msg.Context.SpaceType,
		SenderID:         msg.Context.SenderID,
		SenderUsername:   msg.Sender.Username,
		SenderName:       msg.Sender.DisplayName,
		MessageID:        msg.Context.MessageID,
		ReplyToMessageID: msg.Context.ReplyToMessageID,
		Mentioned:        msg.Context.Mentioned,
		Text:             msg.Content,
		Media:            append([]string(nil), msg.Media...),
		ReplyHandles:     cloneStringMap(msg.Context.ReplyHandles),
		Raw:              cloneStringMap(msg.Context.Raw),
	}
}

func (al *AgentLoop) publishWorkflowTriggered(ref string, msg bus.InboundMessage, match *workflows.ChannelMessageMatch) {
	msg = bus.NormalizeInboundMessage(msg)
	sessionKey := ""
	if match != nil {
		sessionKey = match.Session
	}
	al.publishRuntimeEvent(runtimeevents.Event{
		Kind:   runtimeevents.KindWorkflowTriggered,
		Source: runtimeevents.Source{Component: "workflow", Name: ref},
		Scope: runtimeevents.Scope{
			SessionKey: sessionKey,
			Channel:    msg.Context.Channel,
			Account:    msg.Context.Account,
			ChatID:     msg.Context.ChatID,
			TopicID:    msg.Context.TopicID,
			SpaceID:    msg.Context.SpaceID,
			SpaceType:  msg.Context.SpaceType,
			ChatType:   msg.Context.ChatType,
			SenderID:   msg.Context.SenderID,
			MessageID:  msg.Context.MessageID,
		},
		Severity: runtimeevents.SeverityInfo,
		Payload: map[string]any{
			"workflow_ref": ref,
			"passthrough":  match != nil && match.Passthrough,
		},
	})
}
