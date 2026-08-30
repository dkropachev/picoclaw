package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type ConversationMessageRequest struct {
	WorkspaceID       string
	ExpectedRevision  int64
	RequestID         string
	Mode              string
	Content           string
	CandidateRevision string
}

type ConversationPage struct {
	Revision int64     `json:"revision"`
	Messages []Message `json:"messages"`
}

func (service *Service) Conversation(ctx context.Context, workspaceID string) (ConversationPage, error) {
	aggregate, err := service.Get(ctx, workspaceID)
	if err != nil {
		return ConversationPage{}, err
	}
	messages := append([]Message(nil), aggregate.Messages...)
	return ConversationPage{Revision: int64(len(messages)), Messages: messages}, nil
}

func (service *Service) SendConversationMessage(
	ctx context.Context,
	request ConversationMessageRequest,
) (ConversationPage, error) {
	if !validOpaqueID(request.WorkspaceID, "devw_") || !validRequestID(request.RequestID) ||
		(request.Mode != "ask" && request.Mode != "steer") ||
		!validBoundedText(request.Content, maxCharterTextBytes, false) || request.ExpectedRevision < 0 {
		return ConversationPage{}, ErrInvalid
	}
	// Chat and implementation share immutable aggregate evidence. Serialize
	// their read/classify/CAS windows so an Ask or Steer cannot invalidate an
	// implementation after it has already changed code.
	if !service.claimImplementation(request.WorkspaceID) {
		return ConversationPage{}, ErrConflict
	}
	defer service.releaseImplementation(request.WorkspaceID)
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return ConversationPage{}, err
	}
	if int64(len(aggregate.Messages)) != request.ExpectedRevision {
		return ConversationPage{Revision: int64(len(aggregate.Messages)), Messages: aggregate.Messages}, ErrConflict
	}
	var candidate *CandidateEvidence
	if request.CandidateRevision != "" {
		repair, found := latestBrowsableRepair(aggregate)
		if !found || repair.CandidateSHA != request.CandidateRevision || service.candidateEvidence == nil {
			return ConversationPage{}, ErrConflict
		}
		evidence, loadErr := service.candidateEvidence.LoadCandidateEvidence(ctx, repair)
		if loadErr != nil || evidence.CandidateSHA != request.CandidateRevision {
			return ConversationPage{}, ErrConflict
		}
		candidate = &evidence
	}
	now := service.now().UTC()
	charter, _ := aggregate.ActiveCharter()
	user := Message{
		ID:   stableID("pms_", request.WorkspaceID, request.RequestID, "user"),
		Role: "user", Stage: "implementation", Mode: request.Mode, Status: "queued",
		Content: strings.TrimSpace(request.Content), CharterID: charter.ID,
		HeadSHA: aggregate.ProviderSnapshot.HeadSHA, CreatedAt: now,
	}
	messages := []Message{user}
	if request.Mode == "steer" {
		if service.ai.Runner == nil {
			return ConversationPage{}, errors.New("development steering classifier is unavailable")
		}
		classificationEvidence := struct {
			Charter   Charter            `json:"charter"`
			Findings  []Finding          `json:"findings"`
			Candidate *CandidateEvidence `json:"candidate,omitempty"`
			Steering  string             `json:"steering"`
		}{charter, aggregate.Findings, candidate, user.Content}
		raw, marshalErr := json.Marshal(classificationEvidence)
		if marshalErr != nil {
			return ConversationPage{}, marshalErr
		}
		execution, runErr := service.ai.Runner.RunIsolated(ctx, IsolatedAIRequest{
			Operation:    "development.steer.classify",
			SystemPrompt: "Decide whether the steering request can be applied wholly inside the confirmed charter and primary change type. CI/CD, release, deployment, dependency upgrades, migrations, generated code, broad cleanup, new behavior, and unrelated edits are scope changes unless explicitly named by the charter. Return only structured fields. Do not authorize edits.",
			UserPrompt:   string(raw),
			Schema: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []any{"scope_change", "explanation"},
				"properties": map[string]any{
					"scope_change": map[string]any{"type": "boolean"},
					"explanation":  map[string]any{"type": "string"},
				},
			},
		})
		if runErr != nil {
			return ConversationPage{}, runErr
		}
		scopeChange, ok := execution.Structured["scope_change"].(bool)
		explanation, explanationOK := execution.Structured["explanation"].(string)
		if !ok || !explanationOK || !validBoundedText(explanation, maxCharterTextBytes, false) {
			return ConversationPage{}, errors.New("development steering classification is invalid")
		}
		if scopeChange {
			user.Status = "needs_clarification"
			messages[0] = user
			messages = append(messages, Message{
				ID:   stableID("pms_", request.WorkspaceID, request.RequestID, "scope"),
				Role: "assistant", Stage: "implementation", Mode: "steer", Status: "needs_clarification",
				Content: strings.TrimSpace(explanation), CharterID: charter.ID,
				HeadSHA: aggregate.ProviderSnapshot.HeadSHA, CreatedAt: now,
			})
		}
	}
	if request.Mode == "ask" {
		if service.ai.Runner == nil {
			return ConversationPage{}, errors.New("development conversation AI is unavailable")
		}
		evidence := struct {
			Provider  ProviderSnapshot   `json:"provider"`
			Charter   Charter            `json:"charter"`
			Findings  []Finding          `json:"findings"`
			Question  string             `json:"question"`
			Candidate *CandidateEvidence `json:"candidate,omitempty"`
		}{aggregate.ProviderSnapshot, charter, aggregate.Findings, user.Content, candidate}
		raw, marshalErr := json.Marshal(evidence)
		if marshalErr != nil {
			return ConversationPage{}, marshalErr
		}
		execution, runErr := service.ai.Runner.RunIsolated(ctx, IsolatedAIRequest{
			Operation:    "development.ask",
			SystemPrompt: "Answer the user's development question from the supplied frozen workspace evidence. Do not claim to edit code, execute commands, change scope, approve gates, or publish anything. State when repository content is unavailable.",
			UserPrompt:   string(raw),
			Schema: map[string]any{
				"type": "object", "additionalProperties": false, "required": []any{"reply"},
				"properties": map[string]any{"reply": map[string]any{"type": "string"}},
			},
		})
		if runErr != nil {
			return ConversationPage{}, runErr
		}
		reply, ok := execution.Structured["reply"].(string)
		if !ok || !validBoundedText(reply, maxCharterTextBytes, false) {
			return ConversationPage{}, errors.New("development conversation response is invalid")
		}
		user.Status = "answered"
		messages[0] = user
		messages = append(messages, Message{
			ID:   stableID("pms_", request.WorkspaceID, request.RequestID, "assistant"),
			Role: "assistant", Stage: "implementation", Mode: "ask", Status: "answered",
			Content: strings.TrimSpace(reply), CharterID: charter.ID,
			HeadSHA: aggregate.ProviderSnapshot.HeadSHA, CreatedAt: now,
		})
	}
	patch := AggregatePatch{
		AppendMessages: messages,
		Activity: []Activity{{
			Kind: "conversation.message", Actor: "user", EntityID: user.ID,
			Summary: "Development conversation message recorded", CreatedAt: now,
		}},
	}
	if request.Mode == "steer" && user.Status == "queued" {
		switch aggregate.Workspace.Phase {
		case PhaseTriage:
			// Next implementation admission consumes the queued message.
		case PhaseImplementation:
			// A terminal implementation attempt needs to be admitted again so
			// the durable worker can consume this steering message.
			if aggregate.Workspace.ExecutionState == ExecutionFailed ||
				aggregate.Workspace.ExecutionState == ExecutionBlocked {
				state := ExecutionQueued
				patch.ExecutionState = &state
			}
		case PhaseValidation, PhaseCompletionAudit, PhasePublication:
			for _, publication := range aggregate.Publications {
				if publication.Kind == PublicationBranchPush &&
					(publication.State == ExecutionRunning || publication.State == ExecutionSucceeded || publication.State == ExecutionUnknown) {
					return ConversationPage{}, ErrConflict
				}
				if publication.Kind == PublicationBranchPush &&
					(publication.State == ExecutionQueued || publication.State == ExecutionWaitingGate) {
					publication.State, publication.PublicErrorCode = ExecutionStale, "steering_invalidated_candidate"
					publication.UpdatedAt = now
					patch.ReplacePublications = append(patch.ReplacePublications, publication)
				}
			}
			phase, state := PhaseImplementation, ExecutionQueued
			patch.Phase, patch.ExecutionState = &phase, &state
		default:
			return ConversationPage{}, ErrConflict
		}
	}
	result, mutateErr := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: request.RequestID,
		Patch:     patch,
	})
	if mutateErr != nil {
		return ConversationPage{}, mutateErr
	}
	return ConversationPage{Revision: int64(len(result.Aggregate.Messages)), Messages: result.Aggregate.Messages}, nil
}
