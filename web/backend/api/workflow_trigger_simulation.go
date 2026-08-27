package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	workflowTriggerSimulationRequestMaxBytes  = 1 << 20
	workflowTriggerSimulationResponseMaxBytes = 1 << 20
	workflowTriggerJSONKeyMaxBytes            = 1 << 10
	workflowTriggerJSONStringMaxBytes         = 64 << 10
	workflowTriggerJSONMaxValues              = 4096
	workflowTriggerOpaqueStringMaxBytes       = 4 << 10
	workflowTriggerReviewTokenMaxBytes        = 4096
	workflowTriggerReviewTTL                  = 5 * time.Minute
)

var workflowTriggerSimulationRandomRead = rand.Read

var workflowTriggerReviewEncoding = base64.RawURLEncoding.Strict()

var admitWorkflowTriggerDevelopmentTestRun = func(
	workspace string,
	admission workflows.WorkflowDevelopmentTestRunAdmission,
	start func() (backgroundWorkflowStart, error),
	options ...workflows.LocalOption,
) (
	*workflows.WorkflowDevelopmentSession,
	bool,
	backgroundWorkflowStart,
	error,
) {
	return workflows.AdmitWorkflowDevelopmentTestRun(
		workspace,
		admission,
		start,
		options...,
	)
}

type workflowTriggerSimulationRequest struct {
	SessionID               string                     `json:"session_id"`
	ExpectedSessionRevision string                     `json:"expected_session_revision"`
	ExpectedDraftRevision   string                     `json:"expected_draft_revision"`
	Prompt                  string                     `json:"prompt"`
	TargetRef               string                     `json:"target_ref"`
	YAML                    string                     `json:"yaml"`
	Trigger                 workflowTriggerRequestWire `json:"trigger"`
	Scenario                json.RawMessage            `json:"scenario"`
}

type workflowTriggerExecutionRequest struct {
	SessionID               string                     `json:"session_id"`
	ExpectedSessionRevision string                     `json:"expected_session_revision"`
	ExpectedDraftRevision   string                     `json:"expected_draft_revision"`
	Prompt                  string                     `json:"prompt"`
	TargetRef               string                     `json:"target_ref"`
	YAML                    string                     `json:"yaml"`
	Trigger                 workflowTriggerRequestWire `json:"trigger"`
	Scenario                json.RawMessage            `json:"scenario"`
	ReviewToken             string                     `json:"review_token"`
}

func (request workflowTriggerExecutionRequest) simulationRequest() workflowTriggerSimulationRequest {
	return workflowTriggerSimulationRequest{
		SessionID:               request.SessionID,
		ExpectedSessionRevision: request.ExpectedSessionRevision,
		ExpectedDraftRevision:   request.ExpectedDraftRevision,
		Prompt:                  request.Prompt,
		TargetRef:               request.TargetRef,
		YAML:                    request.YAML,
		Trigger:                 request.Trigger,
		Scenario:                append(json.RawMessage(nil), request.Scenario...),
	}
}

type workflowTriggerRequestWire struct {
	Type          workflows.WorkflowTriggerKind `json:"type"`
	ScheduleIndex *int                          `json:"schedule_index,omitempty"`
}

type workflowTriggerInvocationScenarioWire struct {
	Inputs   map[string]any     `json:"inputs"`
	Secrets  map[string]string  `json:"secrets"`
	Session  string             `json:"session"`
	Delivery workflows.Delivery `json:"delivery"`
}

type workflowTriggerScheduleScenarioWire struct {
	ScheduledAt string `json:"scheduled_at"`
}

type workflowTriggerMessageScenarioWire struct {
	Message workflowTriggerMessageWire `json:"message"`
}

type workflowTriggerMessageWire struct {
	Channel          string            `json:"channel"`
	Account          string            `json:"account"`
	ChatID           string            `json:"chat_id"`
	ChatType         string            `json:"chat_type"`
	TopicID          string            `json:"topic_id"`
	SpaceID          string            `json:"space_id"`
	SpaceType        string            `json:"space_type"`
	SenderID         string            `json:"sender_id"`
	SenderUsername   string            `json:"sender_username"`
	SenderName       string            `json:"sender_name"`
	MessageID        string            `json:"message_id"`
	ReplyToMessageID string            `json:"reply_to_message_id"`
	Mentioned        bool              `json:"mentioned"`
	Text             string            `json:"text"`
	Media            []string          `json:"media"`
	ReplyHandles     map[string]string `json:"reply_handles"`
	Raw              map[string]string `json:"raw"`
}

type workflowTriggerRuntimeEventScenarioWire struct {
	Event runtimeevents.Event `json:"event"`
}

type workflowTriggerEventScenarioWire struct {
	EventID string `json:"event_id"`
}

type workflowTriggerSimulationResponse struct {
	Simulation  workflows.WorkflowTriggerSimulationStatus `json:"simulation"`
	Review      workflows.WorkflowTriggerEffectReview     `json:"review"`
	ReviewToken string                                    `json:"review_token,omitempty"`
}

type workflowTriggerSimulationErrorResponse struct {
	Error string `json:"error"`
}

type workflowTriggerReviewTokenPayload struct {
	Version   int    `json:"v"`
	ExpiresAt int64  `json:"exp"`
	Nonce     string `json:"nonce"`
}

type workflowTriggerReviewBinding struct {
	ConfigRevision string `json:"config_revision"`
	EventDigest    string `json:"event_digest"`
}

var (
	errWorkflowTriggerSimulationRequest = errors.New("invalid workflow trigger simulation request")
	errWorkflowTriggerReviewToken       = errors.New("invalid workflow trigger review token")
	errWorkflowTriggerReviewConsumed    = errors.New("workflow trigger review token already consumed")
	errWorkflowTriggerConfigMismatch    = errors.New("workflow trigger execution config changed")
	errWorkflowTriggerConfigUnavailable = errors.New("workflow trigger execution config unavailable")
)

func (h *Handler) workflowTriggerReviewSigningKey() ([32]byte, error) {
	if h == nil {
		return [32]byte{}, errWorkflowTriggerReviewToken
	}
	h.workflowTriggerReviewOnce.Do(func() {
		var count int
		count, h.workflowTriggerReviewErr = workflowTriggerSimulationRandomRead(
			h.workflowTriggerReviewKey[:],
		)
		if h.workflowTriggerReviewErr == nil &&
			count != len(h.workflowTriggerReviewKey) {
			h.workflowTriggerReviewErr = io.ErrUnexpectedEOF
		}
	})
	if h.workflowTriggerReviewErr != nil {
		return [32]byte{}, errWorkflowTriggerReviewToken
	}
	return h.workflowTriggerReviewKey, nil
}

func (h *Handler) workflowTriggerReviewTokenWasConsumed(token string) bool {
	identity, _, ok := workflowTriggerReviewTokenIdentity(token)
	if h == nil || !ok {
		return false
	}
	h.workflowTriggerReviewUseMu.Lock()
	defer h.workflowTriggerReviewUseMu.Unlock()
	h.pruneWorkflowTriggerReviewUsesLocked()
	_, consumed := h.workflowTriggerReviewUsed[identity]
	if consumed {
		return true
	}
	return false
}

func (h *Handler) consumeWorkflowTriggerReviewToken(token string) bool {
	identity, expiresAt, ok := workflowTriggerReviewTokenIdentity(token)
	if h == nil || !ok {
		return false
	}
	h.workflowTriggerReviewUseMu.Lock()
	defer h.workflowTriggerReviewUseMu.Unlock()
	h.pruneWorkflowTriggerReviewUsesLocked()
	if _, consumed := h.workflowTriggerReviewUsed[identity]; consumed {
		return false
	}
	if h.workflowTriggerReviewUsed == nil {
		h.workflowTriggerReviewUsed = make(map[[32]byte]int64)
	}
	h.workflowTriggerReviewUsed[identity] = expiresAt
	return true
}

func (h *Handler) pruneWorkflowTriggerReviewUsesLocked() {
	if h == nil || h.workflowTriggerReviewNow == nil {
		return
	}
	now := h.workflowTriggerReviewNow().UTC().Unix()
	for identity, expiresAt := range h.workflowTriggerReviewUsed {
		if expiresAt <= now {
			delete(h.workflowTriggerReviewUsed, identity)
		}
	}
}

func workflowTriggerReviewTokenIdentity(
	token string,
) ([32]byte, int64, bool) {
	payloadBytes, providedMAC, payload, ok := parseWorkflowTriggerReviewToken(token)
	if !ok || payload.ExpiresAt <= 0 {
		return [32]byte{}, 0, false
	}
	return workflowTriggerReviewTokenDigest(payloadBytes, providedMAC),
		payload.ExpiresAt,
		true
}

func parseWorkflowTriggerReviewToken(
	token string,
) (
	[]byte,
	[]byte,
	workflowTriggerReviewTokenPayload,
	bool,
) {
	if token == "" || len(token) > workflowTriggerReviewTokenMaxBytes {
		return nil, nil, workflowTriggerReviewTokenPayload{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, nil, workflowTriggerReviewTokenPayload{}, false
	}
	payloadBytes, err := workflowTriggerReviewEncoding.DecodeString(parts[0])
	if err != nil ||
		len(payloadBytes) > 512 ||
		workflowTriggerReviewEncoding.EncodeToString(payloadBytes) != parts[0] {
		return nil, nil, workflowTriggerReviewTokenPayload{}, false
	}
	providedMAC, err := workflowTriggerReviewEncoding.DecodeString(parts[1])
	if err != nil ||
		len(providedMAC) != sha256.Size ||
		workflowTriggerReviewEncoding.EncodeToString(providedMAC) != parts[1] {
		return nil, nil, workflowTriggerReviewTokenPayload{}, false
	}
	var payload workflowTriggerReviewTokenPayload
	if decodeWorkflowJobsJSON(payloadBytes, &payload, true) != nil ||
		payload.Version != 1 ||
		payload.ExpiresAt <= 0 ||
		payload.Nonce == "" ||
		len(payload.Nonce) > 64 {
		return nil, nil, workflowTriggerReviewTokenPayload{}, false
	}
	nonce, err := workflowTriggerReviewEncoding.DecodeString(payload.Nonce)
	if err != nil ||
		len(nonce) != 16 ||
		workflowTriggerReviewEncoding.EncodeToString(nonce) != payload.Nonce {
		return nil, nil, workflowTriggerReviewTokenPayload{}, false
	}
	return payloadBytes, providedMAC, payload, true
}

func workflowTriggerReviewTokenDigest(parts ...[]byte) [32]byte {
	digest := sha256.New()
	for _, part := range parts {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write(part)
	}
	var identity [sha256.Size]byte
	copy(identity[:], digest.Sum(nil))
	return identity
}

func (h *Handler) tryLockWorkflowTriggerDevelopment(
	w http.ResponseWriter,
) func() {
	if h == nil || !h.workflowDevelopmentMu.TryLock() {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_development_busy",
		)
		return nil
	}
	return h.workflowDevelopmentMu.Unlock
}

func (h *Handler) handleSimulateWorkflowTrigger(w http.ResponseWriter, r *http.Request) {
	var request workflowTriggerSimulationRequest
	if !decodeWorkflowTriggerSimulationRequest(
		w,
		r,
		&request,
		[]string{
			"session_id",
			"expected_session_revision",
			"expected_draft_revision",
			"prompt",
			"target_ref",
			"yaml",
			"trigger",
			"scenario",
		},
	) {
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	unlock := h.tryLockWorkflowTriggerDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()

	cfg, configRevision, err := config.LoadCurrentConfigSnapshot(h.configPath)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_simulation_unavailable",
		)
		return
	}
	workspace := cfg.WorkspacePath()
	session, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_simulation_unavailable",
		)
		return
	}
	if session == nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusNotFound,
			"workflow_development_session_not_found",
		)
		return
	}
	if !workflowTriggerSimulationFenceMatches(request, session) {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_development_fence_mismatch",
		)
		return
	}
	input, err := h.workflowTriggerSimulationInput(r.Context(), request, cfg)
	if err != nil {
		h.writeWorkflowTriggerSimulationInputError(w, err)
		return
	}
	simulation, err := workflows.SimulateWorkflowTrigger(input)
	if err != nil {
		h.writeWorkflowTriggerSimulationInputError(w, err)
		return
	}
	binding, err := workflowTriggerReviewBindingForInput(
		configRevision,
		input,
	)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_simulation_unavailable",
		)
		return
	}
	response := workflowTriggerSimulationResponse{
		Simulation: simulation.Simulation,
		Review:     simulation.Review,
	}
	if simulation.Simulation.Executable {
		response.ReviewToken, err = h.issueWorkflowTriggerReviewToken(
			request,
			simulation,
			binding,
		)
		if err != nil {
			writeWorkflowTriggerSimulationError(
				w,
				http.StatusServiceUnavailable,
				"workflow_trigger_simulation_unavailable",
			)
			return
		}
	}
	writeWorkflowTriggerSimulationJSON(w, http.StatusOK, response)
}

func workflowTriggerSimulationFenceMatches(
	request workflowTriggerSimulationRequest,
	session *workflows.WorkflowDevelopmentSession,
) bool {
	return session != nil &&
		request.SessionID != "" &&
		request.SessionID == session.ID &&
		request.ExpectedSessionRevision != "" &&
		request.ExpectedSessionRevision == session.SessionRevision &&
		request.ExpectedDraftRevision != "" &&
		request.ExpectedDraftRevision == session.DraftRevision
}

func (h *Handler) workflowTriggerSimulationInput(
	ctx context.Context,
	request workflowTriggerSimulationRequest,
	cfg *config.Config,
) (workflows.WorkflowTriggerSimulationInput, error) {
	if !request.Trigger.Type.Valid() ||
		request.SessionID == "" ||
		request.ExpectedSessionRevision == "" ||
		request.ExpectedDraftRevision == "" ||
		request.TargetRef == "" ||
		strings.TrimSpace(request.TargetRef) != request.TargetRef ||
		request.Scenario == nil {
		return workflows.WorkflowTriggerSimulationInput{},
			errWorkflowTriggerSimulationRequest
	}
	if request.Trigger.Type == workflows.WorkflowTriggerSchedule {
		if !workflowTriggerScheduleIndexValid(request.Trigger.ScheduleIndex) {
			return workflows.WorkflowTriggerSimulationInput{},
				errWorkflowTriggerSimulationRequest
		}
	} else if request.Trigger.ScheduleIndex != nil {
		return workflows.WorkflowTriggerSimulationInput{},
			errWorkflowTriggerSimulationRequest
	}
	input := workflows.WorkflowTriggerSimulationInput{
		YAML:        request.YAML,
		WorkflowRef: request.TargetRef,
		Trigger: workflows.WorkflowTriggerSelector{
			Kind:          request.Trigger.Type,
			ScheduleIndex: request.Trigger.ScheduleIndex,
		},
	}
	switch request.Trigger.Type {
	case workflows.WorkflowTriggerManual,
		workflows.WorkflowTriggerWorkflowCall:
		var scenario workflowTriggerInvocationScenarioWire
		if err := decodeWorkflowTriggerScenarioAllowed(
			request.Scenario,
			nil,
			[]string{"inputs", "secrets", "session", "delivery"},
			&scenario,
		); err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		if err := validateWorkflowTriggerInvocationMembers(
			request.Scenario,
		); err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		if scenario.Inputs != nil {
			normalized, err := normalizeWorkflowTriggerJSONValue(
				scenario.Inputs,
			)
			if err != nil {
				return workflows.WorkflowTriggerSimulationInput{},
					errWorkflowTriggerSimulationRequest
			}
			scenario.Inputs = normalized.(map[string]any)
		}
		input.Scenario.Invocation = &workflows.WorkflowTriggerInvocation{
			Inputs:   scenario.Inputs,
			Secrets:  scenario.Secrets,
			Session:  scenario.Session,
			Delivery: scenario.Delivery,
		}
	case workflows.WorkflowTriggerSchedule:
		var scenario workflowTriggerScheduleScenarioWire
		if err := decodeWorkflowTriggerScenario(
			request.Scenario,
			[]string{"scheduled_at"},
			&scenario,
		); err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		scheduledAt, err := parseWorkflowTriggerScheduledAt(scenario.ScheduledAt)
		if err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		input.Scenario.ScheduledAt = &scheduledAt
	case workflows.WorkflowTriggerChannelMessage,
		workflows.WorkflowTriggerCommand:
		var scenario workflowTriggerMessageScenarioWire
		if err := decodeWorkflowTriggerScenario(
			request.Scenario,
			[]string{"message"},
			&scenario,
		); err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		messageRaw, err := workflowTriggerScenarioMember(
			request.Scenario,
			"message",
		)
		if err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		if err := validateWorkflowTriggerMessage(messageRaw); err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		input.Scenario.Message = scenario.Message.workflowEvent()
	case workflows.WorkflowTriggerRuntimeEvent:
		var scenario workflowTriggerRuntimeEventScenarioWire
		if err := decodeWorkflowTriggerScenario(
			request.Scenario,
			[]string{"event"},
			&scenario,
		); err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		eventRaw, err := workflowTriggerScenarioMember(
			request.Scenario,
			"event",
		)
		if err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		if err := validateWorkflowTriggerRuntimeEvent(eventRaw); err != nil {
			return workflows.WorkflowTriggerSimulationInput{},
				errWorkflowTriggerSimulationRequest
		}
		if scenario.Event.Payload != nil {
			normalized, err := normalizeWorkflowTriggerJSONValue(
				scenario.Event.Payload,
			)
			if err != nil {
				return workflows.WorkflowTriggerSimulationInput{},
					errWorkflowTriggerSimulationRequest
			}
			scenario.Event.Payload = normalized
		}
		if scenario.Event.Attrs != nil {
			normalized, err := normalizeWorkflowTriggerJSONValue(
				scenario.Event.Attrs,
			)
			if err != nil {
				return workflows.WorkflowTriggerSimulationInput{},
					errWorkflowTriggerSimulationRequest
			}
			scenario.Event.Attrs = normalized.(map[string]any)
		}
		input.Scenario.RuntimeEvent = &scenario.Event
	case workflows.WorkflowTriggerEvent:
		var scenario workflowTriggerEventScenarioWire
		if err := decodeWorkflowTriggerScenario(
			request.Scenario,
			[]string{"event_id"},
			&scenario,
		); err != nil ||
			scenario.EventID == "" ||
			strings.TrimSpace(scenario.EventID) != scenario.EventID ||
			!validOperatorEventID(scenario.EventID) {
			return workflows.WorkflowTriggerSimulationInput{},
				errWorkflowTriggerSimulationRequest
		}
		envelope, err := h.loadWorkflowEventEnvelopeReadOnly(
			ctx,
			scenario.EventID,
			true,
			cfg,
		)
		if err != nil {
			return workflows.WorkflowTriggerSimulationInput{}, err
		}
		input.Scenario.Event = &envelope
	default:
		return workflows.WorkflowTriggerSimulationInput{},
			errWorkflowTriggerSimulationRequest
	}
	return input, nil
}

func workflowTriggerReviewBindingForInput(
	configRevision string,
	input workflows.WorkflowTriggerSimulationInput,
) (workflowTriggerReviewBinding, error) {
	if strings.TrimSpace(configRevision) == "" {
		return workflowTriggerReviewBinding{},
			errWorkflowTriggerConfigUnavailable
	}
	binding := workflowTriggerReviewBinding{
		ConfigRevision: configRevision,
	}
	if input.Trigger.Kind != workflows.WorkflowTriggerEvent {
		return binding, nil
	}
	if input.Scenario.Event == nil {
		return workflowTriggerReviewBinding{},
			errWorkflowTriggerSimulationRequest
	}
	canonical, err := canonicalWorkflowTriggerPrivateValue(
		input.Scenario.Event,
	)
	if err != nil {
		return workflowTriggerReviewBinding{}, err
	}
	digest := sha256.Sum256(canonical)
	binding.EventDigest = base64.RawURLEncoding.EncodeToString(digest[:])
	return binding, nil
}

func canonicalWorkflowTriggerPrivateValue(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func (message workflowTriggerMessageWire) workflowEvent() *workflows.ChannelMessageEvent {
	return &workflows.ChannelMessageEvent{
		Channel:          message.Channel,
		Account:          message.Account,
		ChatID:           message.ChatID,
		ChatType:         message.ChatType,
		TopicID:          message.TopicID,
		SpaceID:          message.SpaceID,
		SpaceType:        message.SpaceType,
		SenderID:         message.SenderID,
		SenderUsername:   message.SenderUsername,
		SenderName:       message.SenderName,
		MessageID:        message.MessageID,
		ReplyToMessageID: message.ReplyToMessageID,
		Mentioned:        message.Mentioned,
		Text:             message.Text,
		Media:            append([]string(nil), message.Media...),
		ReplyHandles:     cloneWorkflowTriggerStringMap(message.ReplyHandles),
		Raw:              cloneWorkflowTriggerStringMap(message.Raw),
	}
}

func cloneWorkflowTriggerStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func validateWorkflowTriggerMessage(raw json.RawMessage) error {
	var message workflowTriggerMessageWire
	if err := decodeWorkflowTriggerScenarioAllowed(
		raw,
		nil,
		[]string{
			"channel",
			"account",
			"chat_id",
			"chat_type",
			"topic_id",
			"space_id",
			"space_type",
			"sender_id",
			"sender_username",
			"sender_name",
			"message_id",
			"reply_to_message_id",
			"mentioned",
			"text",
			"media",
			"reply_handles",
			"raw",
		},
		&message,
	); err != nil {
		return err
	}
	return rejectWorkflowTriggerTypedNulls(raw)
}

func validateWorkflowTriggerInvocationMembers(raw json.RawMessage) error {
	members, err := workflowJobsJSONObjectMembers(raw)
	if err != nil {
		return errWorkflowTriggerSimulationRequest
	}
	if secrets, ok := members["secrets"]; ok {
		if err := rejectWorkflowTriggerTypedNulls(secrets); err != nil {
			return err
		}
	}
	if delivery, ok := members["delivery"]; ok {
		var value workflows.Delivery
		if err := decodeWorkflowTriggerScenarioAllowed(
			delivery,
			nil,
			[]string{
				"channel",
				"chat_id",
				"topic_id",
				"thread_ts",
				"message_id",
				"reply_to_message_id",
				"reply_handles",
			},
			&value,
		); err != nil {
			return err
		}
		if err := rejectWorkflowTriggerTypedNulls(delivery); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowTriggerRuntimeEvent(raw json.RawMessage) error {
	var event runtimeevents.Event
	if err := decodeWorkflowTriggerScenarioAllowed(
		raw,
		nil,
		[]string{
			"id",
			"kind",
			"time",
			"source",
			"scope",
			"correlation",
			"severity",
			"payload",
			"attrs",
		},
		&event,
	); err != nil {
		return err
	}
	members, err := workflowJobsJSONObjectMembers(raw)
	if err != nil {
		return errWorkflowTriggerSimulationRequest
	}
	for _, field := range []string{"source", "scope", "correlation"} {
		if member, ok := members[field]; ok {
			if err := rejectWorkflowTriggerTypedNulls(member); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectWorkflowTriggerTypedNulls(raw json.RawMessage) error {
	var value any
	if decodeWorkflowJobsJSON(raw, &value, false) != nil ||
		rejectNullWorkflowInspectionJSONValue(value) != nil {
		return errWorkflowTriggerSimulationRequest
	}
	return nil
}

func workflowTriggerScenarioMember(
	raw json.RawMessage,
	name string,
) (json.RawMessage, error) {
	members, err := workflowJobsJSONObjectMembers(raw)
	if err != nil {
		return nil, errWorkflowTriggerSimulationRequest
	}
	member, ok := members[name]
	if !ok {
		return nil, errWorkflowTriggerSimulationRequest
	}
	return member, nil
}

func decodeWorkflowTriggerScenarioAllowed(
	raw json.RawMessage,
	required []string,
	optional []string,
	destination any,
) error {
	if !utf8.Valid(raw) ||
		!validJSONUnicodeScalars(raw) ||
		rejectDuplicateWorkflowJobsJSONKeys(raw) != nil {
		return errWorkflowTriggerSimulationRequest
	}
	members, err := workflowJobsJSONObjectMembers(raw)
	if err != nil ||
		!workflowJobsAllowedFields(members, required, optional) ||
		workflowTriggerMembersContainNull(members) ||
		decodeWorkflowJobsJSON(raw, destination, true) != nil {
		return errWorkflowTriggerSimulationRequest
	}
	return nil
}

func workflowTriggerMembersContainNull(
	members map[string]json.RawMessage,
) bool {
	for _, value := range members {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return true
		}
	}
	return false
}

func (h *Handler) writeWorkflowTriggerSimulationInputError(
	w http.ResponseWriter,
	err error,
) {
	switch {
	case errors.Is(err, errWorkflowEventInvalid),
		errors.Is(err, errWorkflowTriggerSimulationRequest),
		errors.Is(err, workflows.ErrWorkflowTriggerSimulationInvalidInput),
		errors.Is(err, workflows.ErrWorkflowTriggerSimulationScenario):
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusBadRequest,
			"invalid_workflow_trigger_simulation_request",
		)
	case errors.Is(err, errWorkflowEventNotFound):
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusNotFound,
			"workflow_event_not_found",
		)
	default:
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_simulation_unavailable",
		)
	}
}

func (h *Handler) handleExecuteWorkflowDevelopmentTest(w http.ResponseWriter, r *http.Request) {
	var request workflowTriggerExecutionRequest
	if !decodeWorkflowTriggerSimulationRequest(
		w,
		r,
		&request,
		[]string{
			"session_id",
			"expected_session_revision",
			"expected_draft_revision",
			"prompt",
			"target_ref",
			"yaml",
			"trigger",
			"scenario",
			"review_token",
		},
	) {
		return
	}
	if strings.TrimSpace(request.ReviewToken) == "" ||
		len(request.ReviewToken) > workflowTriggerReviewTokenMaxBytes {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusForbidden,
			"workflow_trigger_review_required",
		)
		return
	}

	// Keep dashboard config writes outside the reviewed execution admission.
	// The config-before-development order matches publish and settings.
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	unlock := h.tryLockWorkflowTriggerDevelopment(w)
	if unlock == nil {
		return
	}
	defer unlock()

	cfg, configRevision, err := config.LoadCurrentConfigSnapshot(h.configPath)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_execution_unavailable",
		)
		return
	}
	workspace := cfg.WorkspacePath()
	session, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_execution_unavailable",
		)
		return
	}
	if session == nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusNotFound,
			"workflow_development_session_not_found",
		)
		return
	}
	simulationRequest := request.simulationRequest()
	if !workflowTriggerSimulationFenceMatches(simulationRequest, session) {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_development_fence_mismatch",
		)
		return
	}
	input, err := h.workflowTriggerSimulationInput(
		r.Context(),
		simulationRequest,
		cfg,
	)
	if err != nil {
		h.writeWorkflowTriggerSimulationInputError(w, err)
		return
	}
	simulation, err := workflows.SimulateWorkflowTrigger(input)
	if err != nil {
		h.writeWorkflowTriggerSimulationInputError(w, err)
		return
	}
	if !simulation.Simulation.Executable {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_trigger_not_executable",
		)
		return
	}
	binding, err := workflowTriggerReviewBindingForInput(
		configRevision,
		input,
	)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_execution_unavailable",
		)
		return
	}
	if tokenErr := h.verifyWorkflowTriggerReviewToken(
		request.ReviewToken,
		simulationRequest,
		simulation,
		binding,
	); tokenErr != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusForbidden,
			"workflow_trigger_review_invalid",
		)
		return
	}
	if h.workflowTriggerReviewTokenWasConsumed(request.ReviewToken) {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_trigger_review_replayed",
		)
		return
	}

	// Reload protected context and repeat the review before any draft or
	// runtime mutation. Event-backed execution therefore uses only the latest
	// server envelope covered by the review token.
	input, err = h.workflowTriggerSimulationInput(
		r.Context(),
		simulationRequest,
		cfg,
	)
	if err != nil {
		h.writeWorkflowTriggerSimulationInputError(w, err)
		return
	}
	simulation, err = workflows.SimulateWorkflowTrigger(input)
	if err != nil || !simulation.Simulation.Executable {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_trigger_not_executable",
		)
		return
	}
	binding, err = workflowTriggerReviewBindingForInput(
		configRevision,
		input,
	)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_execution_unavailable",
		)
		return
	}
	if tokenErr := h.verifyWorkflowTriggerReviewToken(
		request.ReviewToken,
		simulationRequest,
		simulation,
		binding,
	); tokenErr != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusForbidden,
			"workflow_trigger_review_invalid",
		)
		return
	}
	runRequest, ok := simulation.RunRequest()
	if !ok {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_trigger_not_executable",
		)
		return
	}

	runtimeConfig, runStore, executor, err := h.workflowRuntimeFromConfigWithoutPrune(
		r.Context(),
		cfg,
	)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_execution_unavailable",
		)
		return
	}
	backgroundStarted := false
	defer func() {
		if !backgroundStarted {
			closeWorkflowRuntime(executor)
		}
	}()
	if runtimeConfig == nil ||
		runtimeConfig.WorkspacePath() != workspace ||
		!runtimeConfig.Workflows.Enabled {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_trigger_execution_disabled",
		)
		return
	}
	currentConfigRevision, err := config.ConfigRevision(h.configPath)
	if err != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_execution_unavailable",
		)
		return
	}
	if currentConfigRevision != configRevision {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_trigger_execution_config_changed",
		)
		return
	}

	runID := workflows.NewRunID()
	runRequest.RunID = runID
	runRequest.WorkflowRef = "draft:" + request.TargetRef
	draftKey := workflows.WorkflowDevelopmentDraftKey(
		request.TargetRef,
		request.YAML,
	)
	eventID := workflowTriggerSimulationEventID(simulationRequest)
	var initialStateRecorded atomic.Bool
	admitted, recorded, started, admissionErr := admitWorkflowTriggerDevelopmentTestRun(
		workspace,
		workflows.WorkflowDevelopmentTestRunAdmission{
			SessionID:               request.SessionID,
			ExpectedSessionRevision: request.ExpectedSessionRevision,
			ExpectedDraftRevision:   request.ExpectedDraftRevision,
			Prompt:                  request.Prompt,
			TargetWorkflowRef:       request.TargetRef,
			YAML:                    request.YAML,
			EventID:                 eventID,
			RunID:                   runID,
		},
		func() (backgroundWorkflowStart, error) {
			configLocked := false
			var started backgroundWorkflowStart
			configErr := config.WithConfigMutationLock(
				h.configPath,
				func() error {
					configLocked = true
					currentRevision, revisionErr := config.ConfigRevision(
						h.configPath,
					)
					if revisionErr != nil {
						return fmt.Errorf(
							"%w: %v",
							errWorkflowTriggerConfigUnavailable,
							revisionErr,
						)
					}
					if currentRevision != configRevision {
						return errWorkflowTriggerConfigMismatch
					}
					if tokenErr := h.verifyWorkflowTriggerReviewToken(
						request.ReviewToken,
						simulationRequest,
						simulation,
						binding,
					); tokenErr != nil {
						return tokenErr
					}
					// The executor receives only the private request returned
					// by the same current, token-bound simulation. Retaining
					// both mutation locks until OnRunCreated fences the durable
					// run to this exact draft and config generation.
					backgroundStarted = true
					started = startWorkflowRunBackground(
						executor,
						runRequest,
						func(result *workflows.RunResult, runErr error) {
							h.reconcileWorkflowDevelopmentTestCompletion(
								workspace,
								request.SessionID,
								draftKey,
								eventID,
								runID,
								result,
								runErr,
								initialStateRecorded.Load(),
							)
						},
					)
					if started.Run == nil {
						if started.Err != nil {
							return fmt.Errorf(
								"%w: %v",
								errWorkflowTriggerConfigUnavailable,
								started.Err,
							)
						}
						return errWorkflowTriggerConfigUnavailable
					}
					if !h.consumeWorkflowTriggerReviewToken(
						request.ReviewToken,
					) {
						return errWorkflowTriggerReviewConsumed
					}
					if started.Run.ID != runID {
						return fmt.Errorf(
							"%w: durable run identity mismatch",
							errWorkflowTriggerConfigUnavailable,
						)
					}
					return nil
				},
			)
			if configErr != nil && !configLocked {
				configErr = fmt.Errorf(
					"%w: %v",
					errWorkflowTriggerConfigUnavailable,
					configErr,
				)
			}
			return started, configErr
		},
		workflowLocalOptionsFromConfig(cfg)...,
	)
	initialStateRecorded.Store(recorded)
	if started.Run == nil {
		h.writeWorkflowTriggerExecutionMutationError(w, admissionErr)
		return
	}
	// Durable acceptance is complete. Let execution proceed before running
	// best-effort retention maintenance, which may need cross-process store
	// locks and must not widen the accepted-but-not-yet-executing window.
	started.Release()
	if pruneErr := pruneWorkflowRunStore(
		r.Context(),
		runtimeConfig,
		runStore,
	); pruneErr != nil {
		logger.WarnCF(
			"workflows",
			"accepted reviewed workflow run retention maintenance failed",
			map[string]any{
				"run_id": started.Run.ID,
				"error":  pruneErr.Error(),
			},
		)
	}
	runningResult := &workflows.RunResult{
		RunID:  started.Run.ID,
		Status: workflows.RunStatusRunning,
	}
	writeAcceptedReviewedWorkflowDevelopmentTestRun(
		w,
		started,
		admitted,
		runningResult,
		recorded,
		admissionErr,
	)
}

func workflowTriggerSimulationEventID(
	request workflowTriggerSimulationRequest,
) string {
	if request.Trigger.Type != workflows.WorkflowTriggerEvent {
		return ""
	}
	var scenario workflowTriggerEventScenarioWire
	if decodeWorkflowJobsJSON(request.Scenario, &scenario, true) != nil {
		return ""
	}
	return scenario.EventID
}

func (h *Handler) writeWorkflowTriggerExecutionMutationError(
	w http.ResponseWriter,
	err error,
) {
	switch {
	case errors.Is(err, errWorkflowTriggerConfigMismatch):
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_trigger_execution_config_changed",
		)
	case errors.Is(err, errWorkflowTriggerReviewToken):
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusForbidden,
			"workflow_trigger_review_invalid",
		)
	case errors.Is(err, workflows.ErrNoActiveDevelopment):
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusNotFound,
			"workflow_development_session_not_found",
		)
	case errors.Is(err, workflows.ErrWorkflowDevelopmentFenceMismatch),
		errors.Is(err, workflows.ErrDevelopmentBusy):
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_development_fence_mismatch",
		)
	case errors.Is(err, workflows.ErrWorkflowDevelopmentDraftNotReady):
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusConflict,
			"workflow_trigger_not_executable",
		)
	case errors.Is(err, workflows.ErrWorkflowDevelopmentTestAdmissionInvalid):
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusBadRequest,
			"invalid_workflow_trigger_simulation_request",
		)
	case errors.Is(err, errWorkflowTriggerConfigUnavailable):
		fallthrough
	default:
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_execution_unavailable",
		)
	}
}

func writeAcceptedReviewedWorkflowDevelopmentTestRun(
	w http.ResponseWriter,
	started backgroundWorkflowStart,
	session *workflows.WorkflowDevelopmentSession,
	runningResult *workflows.RunResult,
	recorded bool,
	recordErr error,
) {
	started.Release()

	payload := map[string]any{
		"result": runningResult,
	}
	if session != nil {
		payload["session"] = projectWorkflowDevelopmentSession(session)
	}
	if session == nil {
		runID := ""
		if runningResult != nil {
			runID = runningResult.RunID
		}
		payload["reconciliation"] = workflowDevelopmentTestReconciliation{
			State:   "degraded",
			Reason:  "draft_test_response_truncated",
			RunID:   runID,
			Message: "the workflow run was accepted, but its development session could not be returned; retain the editor draft and refresh the durable run",
		}
		if recordErr != nil {
			logger.ErrorCF(
				"workflows",
				"accepted reviewed workflow development session unavailable",
				map[string]any{
					"run_id": runID,
					"error":  recordErr.Error(),
				},
			)
		}
	} else if !recorded {
		runID := ""
		if runningResult != nil {
			runID = runningResult.RunID
		}
		payload["reconciliation"] = workflowDevelopmentTestReconciliation{
			State:   "degraded",
			Reason:  "draft_test_snapshot_not_recorded",
			RunID:   runID,
			Message: "the workflow run was created, but its development snapshot could not be recorded; inspect the durable run and run a current draft test before publishing",
		}
		if recordErr != nil {
			logger.ErrorCF(
				"workflows",
				"failed to record accepted reviewed workflow development test",
				map[string]any{
					"run_id": runID,
					"error":  recordErr.Error(),
				},
			)
		}
	}
	writeAcceptedWorkflowTriggerSimulationJSON(w, payload, runningResult)
}

func writeAcceptedWorkflowTriggerSimulationJSON(
	w http.ResponseWriter,
	payload any,
	runningResult *workflows.RunResult,
) {
	encoded, err := json.Marshal(payload)
	if err == nil &&
		len(encoded)+1 <= workflowTriggerSimulationResponseMaxBytes {
		writeWorkflowTriggerEncodedJSON(w, http.StatusAccepted, encoded)
		return
	}
	runID := ""
	if runningResult != nil {
		runID = runningResult.RunID
	}
	fallback, fallbackErr := json.Marshal(map[string]any{
		"result": &workflows.RunResult{
			RunID:  runID,
			Status: workflows.RunStatusRunning,
		},
		"reconciliation": workflowDevelopmentTestReconciliation{
			State:   "degraded",
			Reason:  "draft_test_response_truncated",
			RunID:   runID,
			Message: "the workflow run was accepted, but its development session was too large to return; retain the editor draft and refresh the durable run",
		},
	})
	if fallbackErr != nil {
		// All fallback fields are fixed strings, so this is defensive only.
		fallback = []byte(
			`{"result":{"run_id":"","status":"running"},"reconciliation":{"state":"degraded","reason":"draft_test_response_truncated","run_id":"","message":"the workflow run was accepted, but its development session was too large to return; retain the editor draft and refresh the durable run"}}`,
		)
	}
	writeWorkflowTriggerEncodedJSON(w, http.StatusAccepted, fallback)
}

func decodeWorkflowTriggerSimulationRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination any,
	fields []string,
) bool {
	if r == nil || r.Body == nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusBadRequest,
			"invalid_workflow_trigger_simulation_request",
		)
		return false
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusUnsupportedMediaType,
			"invalid_workflow_trigger_simulation_request",
		)
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" || len(parameters) > 1 {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusUnsupportedMediaType,
			"invalid_workflow_trigger_simulation_request",
		)
		return false
	}
	if charset, ok := parameters["charset"]; ok &&
		!strings.EqualFold(strings.TrimSpace(charset), "utf-8") {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusUnsupportedMediaType,
			"invalid_workflow_trigger_simulation_request",
		)
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(
		w,
		r.Body,
		workflowTriggerSimulationRequestMaxBytes,
	))
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeWorkflowTriggerSimulationError(
				w,
				http.StatusRequestEntityTooLarge,
				"workflow_trigger_simulation_request_too_large",
			)
			return false
		}
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusBadRequest,
			"invalid_workflow_trigger_simulation_request",
		)
		return false
	}
	members, membersErr := workflowJobsJSONObjectMembers(raw)
	if !utf8.Valid(raw) ||
		!validJSONUnicodeScalars(raw) ||
		validateWorkflowTriggerJSONBounds(raw) != nil ||
		rejectDuplicateWorkflowJobsJSONKeys(raw) != nil ||
		membersErr != nil ||
		!workflowJobsExactFields(members, fields...) ||
		workflowTriggerMembersContainNull(members) ||
		!validWorkflowTriggerSelectorMember(members["trigger"]) ||
		decodeWorkflowJobsJSON(raw, destination, true) != nil {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusBadRequest,
			"invalid_workflow_trigger_simulation_request",
		)
		return false
	}
	return true
}

type workflowTriggerJSONBoundBudget struct {
	values int
}

func validateWorkflowTriggerJSONBounds(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	budget := &workflowTriggerJSONBoundBudget{}
	if err := consumeBoundedWorkflowTriggerJSONValue(
		decoder,
		0,
		"",
		budget,
	); err != nil {
		return err
	}
	return requireWorkflowEventTriggerJSONEOF(decoder)
}

func consumeBoundedWorkflowTriggerJSONValue(
	decoder *json.Decoder,
	depth int,
	directTopLevelField string,
	budget *workflowTriggerJSONBoundBudget,
) error {
	if depth > workflowJobsEditorJSONMaxDepth {
		return errors.New("JSON nesting exceeds limit")
	}
	budget.values++
	if budget.values > workflowTriggerJSONMaxValues {
		return errors.New("JSON value count exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case string:
		return validateWorkflowTriggerJSONStringBound(
			value,
			directTopLevelField,
		)
	case json.Delim:
		switch value {
		case '{':
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				if keyErr != nil {
					return keyErr
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key must be a string")
				}
				if key == "" || len(key) > workflowTriggerJSONKeyMaxBytes {
					return errors.New("JSON object key exceeds limit")
				}
				childTopLevelField := ""
				if depth == 0 {
					childTopLevelField = key
				}
				if err := consumeBoundedWorkflowTriggerJSONValue(
					decoder,
					depth+1,
					childTopLevelField,
					budget,
				); err != nil {
					return err
				}
			}
			closing, closingErr := decoder.Token()
			if closingErr != nil || closing != json.Delim('}') {
				return errors.New("unterminated JSON object")
			}
		case '[':
			for decoder.More() {
				if err := consumeBoundedWorkflowTriggerJSONValue(
					decoder,
					depth+1,
					"",
					budget,
				); err != nil {
					return err
				}
			}
			closing, closingErr := decoder.Token()
			if closingErr != nil || closing != json.Delim(']') {
				return errors.New("unterminated JSON array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	return nil
}

func validateWorkflowTriggerJSONStringBound(
	value string,
	directTopLevelField string,
) error {
	maximumBytes := int64(workflowTriggerJSONStringMaxBytes)
	switch directTopLevelField {
	case "session_id",
		"expected_session_revision",
		"expected_draft_revision":
		maximumBytes = workflowTriggerOpaqueStringMaxBytes
	case "prompt":
		maximumBytes = workflowTriggerJSONStringMaxBytes
	case "target_ref":
		maximumBytes = workflows.MaxWorkflowInspectionSourceRefBytes
	case "yaml":
		maximumBytes = workflows.MaxWorkflowInspectionSourceBytes
	case "review_token":
		maximumBytes = workflowTriggerReviewTokenMaxBytes
	}
	if int64(len(value)) > maximumBytes {
		return errors.New("JSON string exceeds limit")
	}
	return nil
}

func validWorkflowTriggerSelectorMember(raw json.RawMessage) bool {
	var selector workflowTriggerRequestWire
	return decodeWorkflowTriggerScenarioAllowed(
		raw,
		[]string{"type"},
		[]string{"schedule_index"},
		&selector,
	) == nil
}

func decodeWorkflowTriggerScenario(
	raw json.RawMessage,
	fields []string,
	destination any,
) error {
	return decodeWorkflowTriggerScenarioAllowed(
		raw,
		fields,
		nil,
		destination,
	)
}

func canonicalWorkflowTriggerSimulationRequest(
	request workflowTriggerSimulationRequest,
) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(request.Scenario))
	decoder.UseNumber()
	var scenario any
	if err := decoder.Decode(&scenario); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errWorkflowTriggerSimulationRequest
		}
		return nil, err
	}
	return json.Marshal(struct {
		SessionID               string                     `json:"session_id"`
		ExpectedSessionRevision string                     `json:"expected_session_revision"`
		ExpectedDraftRevision   string                     `json:"expected_draft_revision"`
		Prompt                  string                     `json:"prompt"`
		TargetRef               string                     `json:"target_ref"`
		YAML                    string                     `json:"yaml"`
		Trigger                 workflowTriggerRequestWire `json:"trigger"`
		Scenario                any                        `json:"scenario"`
	}{
		SessionID:               request.SessionID,
		ExpectedSessionRevision: request.ExpectedSessionRevision,
		ExpectedDraftRevision:   request.ExpectedDraftRevision,
		Prompt:                  request.Prompt,
		TargetRef:               request.TargetRef,
		YAML:                    request.YAML,
		Trigger:                 request.Trigger,
		Scenario:                scenario,
	})
}

func canonicalWorkflowTriggerReview(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	removeWorkflowTriggerReviewVolatileFields(normalized)
	return json.Marshal(normalized)
}

func removeWorkflowTriggerReviewVolatileFields(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "validated_at")
		for _, child := range typed {
			removeWorkflowTriggerReviewVolatileFields(child)
		}
	case []any:
		for _, child := range typed {
			removeWorkflowTriggerReviewVolatileFields(child)
		}
	}
}

func (h *Handler) issueWorkflowTriggerReviewToken(
	request workflowTriggerSimulationRequest,
	review any,
	binding workflowTriggerReviewBinding,
) (string, error) {
	if h == nil ||
		h.workflowTriggerReviewNow == nil ||
		strings.TrimSpace(binding.ConfigRevision) == "" {
		return "", errWorkflowTriggerReviewToken
	}
	signingKey, err := h.workflowTriggerReviewSigningKey()
	if err != nil {
		return "", err
	}
	requestBytes, err := canonicalWorkflowTriggerSimulationRequest(request)
	if err != nil {
		return "", err
	}
	reviewBytes, err := canonicalWorkflowTriggerReview(review)
	if err != nil {
		return "", err
	}
	bindingBytes, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	var nonce [16]byte
	count, err := workflowTriggerSimulationRandomRead(nonce[:])
	if err != nil {
		return "", err
	}
	if count != len(nonce) {
		return "", io.ErrUnexpectedEOF
	}
	payload := workflowTriggerReviewTokenPayload{
		Version:   1,
		ExpiresAt: h.workflowTriggerReviewNow().UTC().Add(workflowTriggerReviewTTL).Unix(),
		Nonce:     base64.RawURLEncoding.EncodeToString(nonce[:]),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := workflowTriggerReviewMAC(
		signingKey,
		payloadBytes,
		requestBytes,
		reviewBytes,
		bindingBytes,
	)
	return base64.RawURLEncoding.EncodeToString(payloadBytes) + "." +
		base64.RawURLEncoding.EncodeToString(mac), nil
}

func (h *Handler) verifyWorkflowTriggerReviewToken(
	token string,
	request workflowTriggerSimulationRequest,
	review any,
	binding workflowTriggerReviewBinding,
) error {
	if h == nil || h.workflowTriggerReviewNow == nil ||
		strings.TrimSpace(binding.ConfigRevision) == "" ||
		len(token) == 0 || len(token) > workflowTriggerReviewTokenMaxBytes {
		return errWorkflowTriggerReviewToken
	}
	signingKey, err := h.workflowTriggerReviewSigningKey()
	if err != nil {
		return errWorkflowTriggerReviewToken
	}
	payloadBytes, providedMAC, payload, ok := parseWorkflowTriggerReviewToken(token)
	if !ok {
		return errWorkflowTriggerReviewToken
	}
	now := h.workflowTriggerReviewNow().UTC()
	expiresAt := time.Unix(payload.ExpiresAt, 0).UTC()
	if !now.Before(expiresAt) ||
		expiresAt.After(now.Add(workflowTriggerReviewTTL+time.Minute)) {
		return errWorkflowTriggerReviewToken
	}
	requestBytes, err := canonicalWorkflowTriggerSimulationRequest(request)
	if err != nil {
		return errWorkflowTriggerReviewToken
	}
	reviewBytes, err := canonicalWorkflowTriggerReview(review)
	if err != nil {
		return errWorkflowTriggerReviewToken
	}
	bindingBytes, err := json.Marshal(binding)
	if err != nil {
		return errWorkflowTriggerReviewToken
	}
	expectedMAC := workflowTriggerReviewMAC(
		signingKey,
		payloadBytes,
		requestBytes,
		reviewBytes,
		bindingBytes,
	)
	if !hmac.Equal(providedMAC, expectedMAC) {
		return errWorkflowTriggerReviewToken
	}
	return nil
}

func workflowTriggerReviewMAC(
	key [32]byte,
	parts ...[]byte,
) []byte {
	mac := hmac.New(sha256.New, key[:])
	for _, part := range parts {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write(part)
	}
	return mac.Sum(nil)
}

func writeWorkflowTriggerSimulationJSON(
	w http.ResponseWriter,
	status int,
	value any,
) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded)+1 > workflowTriggerSimulationResponseMaxBytes {
		writeWorkflowTriggerSimulationError(
			w,
			http.StatusServiceUnavailable,
			"workflow_trigger_simulation_unavailable",
		)
		return
	}
	writeWorkflowTriggerEncodedJSON(w, status, encoded)
}

func writeWorkflowTriggerEncodedJSON(
	w http.ResponseWriter,
	status int,
	encoded []byte,
) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
	_, _ = w.Write([]byte{'\n'})
}

func writeWorkflowTriggerSimulationError(
	w http.ResponseWriter,
	status int,
	code string,
) {
	encoded, err := json.Marshal(workflowTriggerSimulationErrorResponse{Error: code})
	if err != nil {
		encoded = []byte(`{"error":"workflow_trigger_simulation_unavailable"}`)
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
	_, _ = w.Write([]byte{'\n'})
}

func parseWorkflowTriggerScheduledAt(value string) (time.Time, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return time.Time{}, errWorkflowTriggerSimulationRequest
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errWorkflowTriggerSimulationRequest
	}
	return parsed, nil
}

func workflowTriggerScheduleIndexValid(value *int) bool {
	return value != nil &&
		*value >= 0 &&
		workflows.WorkflowJSONNumberIsBrowserSafe(strconv.Itoa(*value))
}
