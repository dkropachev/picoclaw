package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

var (
	errWorkflowEventInvalid     = errors.New("workflow event ID is invalid")
	errWorkflowEventNotFound    = errors.New("workflow event was not found")
	errWorkflowEventUnavailable = errors.New("workflow event service is unavailable")
)

// Three independently bounded event/actor/subject attribute maps can expand
// under JSON string escaping. Their worst case is below
// 3*128*(256+8192)*6 = 19,464,192 bytes; 24 MiB also covers seven escaped
// 2-KiB entity fields, fixed envelope fields, map syntax, and timestamps.
// The dedicated operator encoder disables HTML expansion inside the payload,
// so the configured payload maximum itself does not need an expansion factor.
const workflowEventContextMetadataAllowanceBytes uint64 = 24 << 20

var workflowEventReadOnlyGatewayPIDData = func(
	h *Handler,
	cfg *config.Config,
) *ppid.PidFileData {
	return h.readOnlyWorkflowEventGatewayPIDData(cfg)
}

// loadWorkflowEventEnvelope reads one already-redacted event through the live
// gateway operator generation. Metadata previews stay payload-free. Full
// workflow context uses one protected upstream request whose controller holds
// the same store generation across metadata and payload reads.
func (h *Handler) loadWorkflowEventEnvelope(
	ctx context.Context,
	eventID string,
	includePayload bool,
) (eventing.Envelope, error) {
	var cfg *config.Config
	if loaded, err := config.LoadConfig(h.configPath); err == nil {
		cfg = loaded
	} else {
		return eventing.Envelope{}, fmt.Errorf(
			"%w: load gateway configuration",
			errWorkflowEventUnavailable,
		)
	}
	return h.loadWorkflowEventEnvelopeWithConfig(
		ctx,
		eventID,
		includePayload,
		cfg,
		eventGatewayPIDData,
	)
}

// loadWorkflowEventEnvelopeReadOnly uses an already-loaded immutable config
// snapshot and peeks process authority without cleaning a stale PID file.
func (h *Handler) loadWorkflowEventEnvelopeReadOnly(
	ctx context.Context,
	eventID string,
	includePayload bool,
	cfg *config.Config,
) (eventing.Envelope, error) {
	return h.loadWorkflowEventEnvelopeWithConfig(
		ctx,
		eventID,
		includePayload,
		cfg,
		workflowEventReadOnlyGatewayPIDData,
	)
}

func (h *Handler) loadWorkflowEventEnvelopeWithConfig(
	ctx context.Context,
	eventID string,
	includePayload bool,
	cfg *config.Config,
	pidDataForConfig func(*Handler, *config.Config) *ppid.PidFileData,
) (eventing.Envelope, error) {
	trimmedEventID := strings.TrimSpace(eventID)
	if eventID != trimmedEventID || !validOperatorEventID(eventID) {
		return eventing.Envelope{}, errWorkflowEventInvalid
	}
	if cfg == nil || pidDataForConfig == nil {
		return eventing.Envelope{}, errWorkflowEventUnavailable
	}
	pidData := pidDataForConfig(h, cfg)
	if !validEventGatewayPIDData(pidData) {
		return eventing.Envelope{}, errWorkflowEventUnavailable
	}

	upstreamPath := "/runtime/eventing/events/" + eventID
	maximum := int64(workflowEventContextMetadataAllowanceBytes)
	if includePayload {
		upstreamPath += "/workflow-context"
	}
	target, err := h.eventGatewayURL(pidData, cfg, upstreamPath, "")
	if err != nil {
		return eventing.Envelope{}, fmt.Errorf(
			"%w: resolve live gateway",
			errWorkflowEventUnavailable,
		)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return eventing.Envelope{}, fmt.Errorf(
			"%w: construct live gateway request",
			errWorkflowEventUnavailable,
		)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+pidData.Token)

	response, err := eventGatewayDo(request, eventGatewayRequestTimeout)
	if err != nil || response == nil || response.Body == nil {
		return eventing.Envelope{}, errWorkflowEventUnavailable
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return eventing.Envelope{}, errWorkflowEventNotFound
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable:
		return eventing.Envelope{}, errWorkflowEventUnavailable
	default:
		return eventing.Envelope{}, errWorkflowEventUnavailable
	}
	payloadBytes := int64(-1)
	if includePayload {
		var limitErr error
		maximum, payloadBytes, limitErr = workflowEventContextResponseLimit(
			response.Header,
		)
		if limitErr != nil {
			return eventing.Envelope{}, errWorkflowEventUnavailable
		}
	}
	if response.ContentLength > maximum {
		return eventing.Envelope{}, errWorkflowEventUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || int64(len(body)) > maximum ||
		!eventGatewayJSONResponse(response.Header.Get("Content-Type"), body) {
		return eventing.Envelope{}, errWorkflowEventUnavailable
	}

	envelope, err := decodeWorkflowEventEnvelope(body, includePayload)
	if err != nil ||
		envelope.ID != eventID ||
		includePayload && int64(len(envelope.Payload)) > payloadBytes {
		return eventing.Envelope{}, errWorkflowEventUnavailable
	}
	return envelope, nil
}

func (h *Handler) readOnlyWorkflowEventGatewayPIDData(
	cfg *config.Config,
) *ppid.PidFileData {
	candidates := make([]*ppid.PidFileData, 0, 2)
	candidates = append(
		candidates,
		cloneEventGatewayPIDData(ppid.PeekPidFile(globalConfigDir())),
	)
	gateway.mu.Lock()
	candidates = append(
		candidates,
		cloneEventGatewayPIDData(gateway.pidData),
	)
	gateway.mu.Unlock()
	for _, candidate := range candidates {
		if !validEventGatewayPIDData(candidate) {
			continue
		}
		ok, _, _ := h.validateGatewayPidData(candidate, cfg)
		if ok {
			return cloneEventGatewayPIDData(candidate)
		}
	}
	return nil
}

func workflowEventContextResponseLimit(
	header http.Header,
) (maximum int64, payloadBytes int64, err error) {
	values := header.Values(eventoperator.WorkflowEventPayloadBytesHeader)
	if len(values) != 1 {
		return 0, 0, errors.New("workflow event payload length header is invalid")
	}
	raw := values[0]
	parsed, parseErr := strconv.ParseUint(raw, 10, 63)
	if parseErr != nil || strconv.FormatUint(parsed, 10) != raw {
		return 0, 0, errors.New("workflow event payload length header is invalid")
	}
	maximumInt64 := ^uint64(0) >> 1
	// loadWorkflowEventEnvelope passes maximum+1 to io.LimitReader.
	if parsed >
		(maximumInt64-1)-workflowEventContextMetadataAllowanceBytes {
		return 0, 0, errors.New("workflow event payload length overflows response bound")
	}
	return int64(parsed + workflowEventContextMetadataAllowanceBytes),
		int64(parsed),
		nil
}

func decodeWorkflowEventEnvelope(
	body []byte,
	includePayload bool,
) (eventing.Envelope, error) {
	var envelope eventing.Envelope
	if includePayload {
		var view eventoperator.WorkflowEventView
		if err := decodeStrictWorkflowEventJSON(body, &view); err != nil {
			return eventing.Envelope{}, err
		}
		envelope = envelopeFromWorkflowEventView(view)
	} else {
		var view eventoperator.EventView
		if err := decodeStrictWorkflowEventJSON(body, &view); err != nil {
			return eventing.Envelope{}, err
		}
		envelope = envelopeFromEventView(view)
		envelope.Payload = json.RawMessage(`{}`)
	}
	if envelope.ID == "" ||
		envelope.Source == "" ||
		envelope.Connector == "" ||
		envelope.Type == "" ||
		envelope.ReceivedAt.IsZero() ||
		(includePayload && len(bytes.TrimSpace(envelope.Payload)) == 0) {
		return eventing.Envelope{}, errors.New("workflow event response is incomplete")
	}

	// The internal projection intentionally has no provider deduplication key.
	// Supply a local validation-only value, then erase it again.
	envelope.DedupeKey = "workflow-context"
	normalized, err := eventing.NormalizeEnvelope(envelope, time.Time{})
	if err != nil {
		return eventing.Envelope{}, err
	}
	normalized.DedupeKey = ""
	if !includePayload {
		normalized.Payload = nil
	}
	return normalized, nil
}

func decodeStrictWorkflowEventJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow event response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func envelopeFromEventView(view eventoperator.EventView) eventing.Envelope {
	return eventing.Envelope{
		ID:         view.ID,
		Source:     view.Source,
		Connector:  view.Connector,
		Type:       view.Type,
		Actor:      eventActorFromView(view.Actor),
		Subject:    eventSubjectFromView(view.Subject),
		OccurredAt: cloneWorkflowEventTime(view.OccurredAt),
		ReceivedAt: view.ReceivedAt,
		Attributes: cloneWorkflowEventStrings(view.Attributes),
		ReplayOf:   view.ReplayOf,
	}
}

func envelopeFromWorkflowEventView(
	view eventoperator.WorkflowEventView,
) eventing.Envelope {
	return eventing.Envelope{
		ID:         view.ID,
		Source:     view.Source,
		Connector:  view.Connector,
		Type:       view.Type,
		Actor:      eventActorFromView(view.Actor),
		Subject:    eventSubjectFromView(view.Subject),
		OccurredAt: cloneWorkflowEventTime(view.OccurredAt),
		ReceivedAt: view.ReceivedAt,
		Payload:    append(json.RawMessage(nil), view.Payload...),
		Attributes: cloneWorkflowEventStrings(view.Attributes),
		ReplayOf:   view.ReplayOf,
	}
}

func eventActorFromView(view *eventoperator.ActorView) *eventing.Actor {
	if view == nil {
		return nil
	}
	return &eventing.Actor{
		ID:          view.ID,
		Type:        view.Type,
		DisplayName: view.DisplayName,
		Attributes:  cloneWorkflowEventStrings(view.Attributes),
	}
}

func eventSubjectFromView(view *eventoperator.SubjectView) *eventing.Subject {
	if view == nil {
		return nil
	}
	return &eventing.Subject{
		ID:         view.ID,
		Type:       view.Type,
		Name:       view.Name,
		URL:        view.URL,
		Attributes: cloneWorkflowEventStrings(view.Attributes),
	}
}

func cloneWorkflowEventTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneWorkflowEventStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
