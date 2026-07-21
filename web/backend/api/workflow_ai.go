package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	agentloop "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowAIReviseRequest struct {
	Prompt    string  `json:"prompt,omitempty"`
	TargetRef string  `json:"target_ref,omitempty"`
	YAML      *string `json:"yaml,omitempty"`
}

type workflowAuthorAgent func(
	context.Context,
	*Handler,
	*workflows.WorkflowDevelopmentSession,
	*workflows.WorkflowDevelopmentValidation,
	[]workflows.Definition,
) (string, error)

type workflowAuthorCapabilities struct {
	Agents []agentloop.AgentDescriptor
	Tools  []string
}

var runWorkflowAuthorAgent workflowAuthorAgent = defaultRunWorkflowAuthorAgent

func (h *Handler) handleAIReviseWorkflowDevelopment(w http.ResponseWriter, r *http.Request) {
	var req workflowAIReviseRequest
	if err := decodeOptionalWorkflowJSON(r, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	unlock := h.tryLockWorkflowDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()
	cfg, err := h.workflowConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := cfg.WorkspacePath()

	reviseReq := workflows.WorkflowDevelopmentReviseRequest{
		Prompt:    req.Prompt,
		TargetRef: req.TargetRef,
		YAML:      req.YAML,
	}
	if _, reviseErr := workflows.ReviseWorkflowDevelopment(workspace, reviseReq); reviseErr != nil {
		writeWorkflowDevelopmentError(w, reviseErr)
		return
	}
	session, err := workflows.ValidateWorkflowDevelopment(workspace)
	if err != nil {
		writeWorkflowDevelopmentError(w, err)
		return
	}
	defs, err := workflows.ListLocal(r.Context(), workspace, workflowLocalOptionsFromConfig(cfg)...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rawResponse, err := runWorkflowAuthorAgent(r.Context(), h, session, session.Validation, defs)
	if err != nil {
		writeWorkflowJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	nextYAML, err := extractWorkflowAuthorYAML(rawResponse)
	if err != nil {
		writeWorkflowJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	_, err = workflows.ReviseWorkflowDevelopment(workspace, workflows.WorkflowDevelopmentReviseRequest{
		YAML: &nextYAML,
	})
	if err != nil {
		writeWorkflowDevelopmentError(w, err)
		return
	}
	session, err = workflows.ValidateWorkflowDevelopment(workspace)
	if err != nil {
		writeWorkflowDevelopmentError(w, err)
		return
	}
	writeWorkflowJSON(w, map[string]any{"session": session})
}

func defaultRunWorkflowAuthorAgent(
	ctx context.Context,
	h *Handler,
	session *workflows.WorkflowDevelopmentSession,
	validation *workflows.WorkflowDevelopmentValidation,
	defs []workflows.Definition,
) (string, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to create provider: %w", err)
	}
	if modelID != "" {
		cfg.Agents.Defaults.ModelName = modelID
	}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	agentLoop := agentloop.NewAgentLoop(
		cfg,
		msgBus,
		provider,
		agentloop.WithConfigPath(h.configPath),
	)
	defer agentLoop.Close()

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return agentLoop.ProcessDirectWithChannel(
		runCtx,
		buildWorkflowAuthorPrompt(
			session,
			validation,
			defs,
			workflowAuthorCapabilitiesFromLoop(agentLoop),
		),
		"workflow-dev:"+session.ID,
		"workflow_dev",
		session.ID,
	)
}

func buildWorkflowAuthorPrompt(
	session *workflows.WorkflowDevelopmentSession,
	validation *workflows.WorkflowDevelopmentValidation,
	defs []workflows.Definition,
	capabilities workflowAuthorCapabilities,
) string {
	var b strings.Builder
	b.WriteString("You are editing a PicoClaw workflow draft.\n")
	b.WriteString("Return only complete workflow YAML. Do not wrap it in markdown. Do not include prose.\n\n")
	b.WriteString("PicoClaw workflow rules:\n")
	b.WriteString("- Local reusable refs use workflows/<name>.yml or workflows/<name>.yaml.\n")
	b.WriteString(
		"- Triggers live under on and may use manual, command, channel_message, schedule, runtime_event, or workflow_call.\n",
	)
	b.WriteString("- Each job must use runs-on: picoclaw with steps, or job-level uses: workflows/<file>.yml.\n")
	b.WriteString("- Job needs may be a string or list. Dependency cycles are invalid.\n")
	b.WriteString(
		"- Dashboard-testable step targets may use agent/, tool/, mcp/, or supported native function/ targets.\n",
	)
	b.WriteString(
		"- Supported native function targets: function/workflow.state, function/workflow.artifact, function/git.inventory.\n",
	)
	b.WriteString(
		"- Prefer native function/ targets over shell scripts for workflow-owned state, artifacts, and git inventory.\n",
	)
	b.WriteString(
		"- Agent steps should put user instructions under with.prompt or with.message and may set history and cache.\n",
	)
	b.WriteString(
		"- Agent steps may declare with.output for JSON structured output; downstream steps should consume steps.<id>.outputs.structured instead of parsing text.\n",
	)
	b.WriteString(
		"- Agent steps may declare with.scope as a list or {items: [...]} and with.managed for generic scope/task splitting, calibration, bounded parallel child runs, model optimization, and effort optimization.\n",
	)
	b.WriteString(
		"- Managed execution requires structured output and is generic; do not encode domain-specific split or merge logic in workflow YAML unless the user asks for it.\n",
	)
	b.WriteString("- Do not invent unsupported top-level keys.\n\n")
	writeWorkflowAuthorCapabilities(&b, capabilities)
	fmt.Fprintf(&b, "Development reason: %s\n", session.Reason)
	fmt.Fprintf(&b, "Target workflow ref: %s\n", session.TargetWorkflowRef)
	if session.SourceWorkflowRef != "" {
		fmt.Fprintf(&b, "Source workflow ref: %s\n", session.SourceWorkflowRef)
	}
	if session.Prompt != "" {
		fmt.Fprintf(&b, "User brief:\n%s\n\n", session.Prompt)
	}
	if len(defs) > 0 {
		b.WriteString("Existing workflow refs:\n")
		for _, def := range defs {
			if def.Ref == "" {
				continue
			}
			if def.Name != "" {
				fmt.Fprintf(&b, "- %s (%s)\n", def.Ref, def.Name)
			} else {
				fmt.Fprintf(&b, "- %s\n", def.Ref)
			}
		}
		b.WriteString("\n")
	}
	if validation != nil && (!validation.Valid || len(validation.Warnings) > 0) {
		b.WriteString("Current validation diagnostics:\n")
		for _, issue := range validation.Errors {
			writeWorkflowPromptIssue(&b, "error", issue)
		}
		for _, issue := range validation.Warnings {
			writeWorkflowPromptIssue(&b, "warning", issue)
		}
		b.WriteString("\n")
	}
	b.WriteString("Current YAML:\n")
	b.WriteString(session.YAML)
	if !strings.HasSuffix(session.YAML, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func workflowAuthorCapabilitiesFromLoop(al *agentloop.AgentLoop) workflowAuthorCapabilities {
	if al == nil {
		return workflowAuthorCapabilities{}
	}
	registry := al.GetRegistry()
	if registry == nil {
		return workflowAuthorCapabilities{}
	}
	capabilities := workflowAuthorCapabilities{
		Agents: registry.ListAgents(""),
	}
	if agent := registry.GetDefaultAgent(); agent != nil && agent.Tools != nil {
		capabilities.Tools = agent.Tools.GetSummaries()
	}
	return capabilities
}

func writeWorkflowAuthorCapabilities(
	b *strings.Builder,
	capabilities workflowAuthorCapabilities,
) {
	if len(capabilities.Agents) == 0 && len(capabilities.Tools) == 0 {
		return
	}
	b.WriteString("Dashboard runtime capabilities:\n")
	if len(capabilities.Agents) > 0 {
		b.WriteString("- Agent step targets must use one of these forms:\n")
		for _, agent := range capabilities.Agents {
			if strings.TrimSpace(agent.ID) == "" {
				continue
			}
			if strings.TrimSpace(agent.Description) != "" {
				fmt.Fprintf(
					b,
					"  - agent/%s - %s\n",
					agent.ID,
					workflowPromptOneLine(agent.Description),
				)
				continue
			}
			fmt.Fprintf(b, "  - agent/%s\n", agent.ID)
		}
	}
	if len(capabilities.Tools) > 0 {
		b.WriteString("- Tool step targets must use one of these visible default-agent tools as tool/<name>:\n")
		limit := len(capabilities.Tools)
		if limit > 24 {
			limit = 24
		}
		for _, summary := range capabilities.Tools[:limit] {
			fmt.Fprintf(b, "  %s\n", workflowPromptOneLine(summary))
		}
		if len(capabilities.Tools) > limit {
			fmt.Fprintf(b, "  - ... %d more tools available\n", len(capabilities.Tools)-limit)
		}
	}
	b.WriteString(
		"- MCP workflow steps use mcp/<server>/<tool>; only use them when the user explicitly asks for an MCP-backed action or the current draft already uses that MCP target.\n\n",
	)
}

func workflowPromptOneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= 180 {
		return value
	}
	return value[:177] + "..."
}

func writeWorkflowPromptIssue(b *strings.Builder, level string, issue workflows.WorkflowValidationIssue) {
	if issue.Path != "" {
		fmt.Fprintf(b, "- %s at %s: %s\n", level, issue.Path, issue.Message)
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", level, issue.Message)
}

var workflowAuthorFencePattern = regexp.MustCompile("(?is)```(?:yaml|yml)?\\s*(.*?)```")

func extractWorkflowAuthorYAML(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("workflow author returned an empty response")
	}
	matches := workflowAuthorFencePattern.FindStringSubmatch(value)
	if len(matches) > 1 {
		value = strings.TrimSpace(matches[1])
	}
	value = trimWorkflowAuthorProse(value)
	if value == "" {
		return "", fmt.Errorf("workflow author did not return YAML")
	}
	return strings.TrimRight(value, " \t\r\n") + "\n", nil
}

func trimWorkflowAuthorProse(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	start := 0
	for start < len(lines) {
		trimmed := strings.TrimSpace(lines[start])
		if strings.HasPrefix(trimmed, "name:") ||
			strings.HasPrefix(trimmed, "on:") ||
			strings.HasPrefix(trimmed, "jobs:") {
			break
		}
		start++
	}
	if start == len(lines) {
		return strings.TrimSpace(value)
	}
	lines = lines[start:]
	end := len(lines)
	for end > 0 {
		trimmed := strings.TrimSpace(lines[end-1])
		if trimmed == "" {
			end--
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines[:end], "\n"))
}
