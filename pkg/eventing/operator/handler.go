package operator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	// RoutePrefix is the operator subtree registered on the gateway's shared
	// HTTP listener.
	RoutePrefix = "/runtime/eventing/"
	// WorkflowEventPayloadBytesHeader carries the exact stored payload length
	// across the protected workflow-context hop.
	WorkflowEventPayloadBytesHeader = "X-Picoclaw-Event-Payload-Bytes"

	maxQueryBytes      = 8 << 10
	maxReplayBodyBytes = 1 << 10

	replayUnknownOutcomeMessage = "replay outcome unknown; inspect events before retrying"
)

type routeKind uint8

const (
	routeUnknown routeKind = iota
	routeEvents
	routeEvent
	routeEventPayload
	routeEventWorkflowContext
	routeDispatches
	routeDispatch
	routeReplay
	routePRWorkspaces
)

type operatorRoute struct {
	kind       routeKind
	eventID    string
	dispatchID string
	method     string
}

func (backend *Backend) serveHTTP(
	w http.ResponseWriter,
	request *http.Request,
	route operatorRoute,
) {
	switch route.kind {
	case routeEvents:
		selection, err := eventListRequestFromQuery(request.URL.RawQuery)
		if err != nil {
			writeOperatorError(w, err)
			return
		}
		page, err := backend.ListEvents(request.Context(), selection)
		if err != nil {
			writeOperatorError(w, err)
			return
		}
		writeOperatorJSON(w, http.StatusOK, page)
	case routeEvent:
		if request.URL.RawQuery != "" {
			writeOperatorError(w, ErrInvalidRequest)
			return
		}
		event, err := backend.GetEvent(request.Context(), route.eventID)
		if err != nil {
			writeOperatorError(w, err)
			return
		}
		writeOperatorJSON(w, http.StatusOK, event)
	case routeEventPayload:
		if request.URL.RawQuery != "" {
			writeOperatorError(w, ErrInvalidRequest)
			return
		}
		payload, err := backend.GetEventPayload(request.Context(), route.eventID)
		if err != nil {
			writeOperatorError(w, err)
			return
		}
		writePayload(w, payload)
	case routeEventWorkflowContext:
		if request.URL.RawQuery != "" {
			writeOperatorError(w, ErrInvalidRequest)
			return
		}
		event, err := backend.GetWorkflowEvent(request.Context(), route.eventID)
		if err != nil {
			writeOperatorError(w, err)
			return
		}
		writeWorkflowEventJSON(w, http.StatusOK, event)
	case routeDispatches:
		selection, err := dispatchListRequestFromQuery(request.URL.RawQuery)
		if err != nil {
			writeOperatorError(w, err)
			return
		}
		page, err := backend.ListDispatches(request.Context(), selection)
		if err != nil {
			writeOperatorError(w, err)
			return
		}
		writeOperatorJSON(w, http.StatusOK, page)
	case routeDispatch:
		if request.URL.RawQuery != "" {
			writeOperatorError(w, ErrInvalidRequest)
			return
		}
		dispatch, err := backend.GetDispatch(request.Context(), route.dispatchID)
		if err != nil {
			writeOperatorError(w, err)
			return
		}
		writeOperatorJSON(w, http.StatusOK, dispatch)
	case routeReplay:
		if request.URL.RawQuery != "" ||
			!jsonRequestContentType(request.Header) ||
			!identityRequestEncoding(request.Header) ||
			!emptyReplayObject(w, request) {
			writeOperatorError(w, ErrInvalidRequest)
			return
		}
		result, err := backend.Replay(request.Context(), route.eventID)
		if err != nil {
			writeReplayOperatorError(w, err)
			return
		}
		w.Header().Set("Location", RoutePrefix+"events/"+result.Event.ID)
		writeOperatorJSON(w, http.StatusCreated, result)
	case routePRWorkspaces:
		if backend.prWorkspaces == nil {
			writeOperatorStatus(w, http.StatusNotFound)
			return
		}
		backend.prWorkspaces.ServeHTTP(w, request)
	default:
		writeOperatorStatus(w, http.StatusNotFound)
	}
}

func routeFromRequest(request *http.Request) operatorRoute {
	if request == nil ||
		request.URL == nil ||
		request.URL.Fragment != "" ||
		request.URL.EscapedPath() != request.URL.Path {
		return operatorRoute{}
	}
	path := request.URL.Path
	switch path {
	case RoutePrefix + "events":
		return operatorRoute{kind: routeEvents, method: http.MethodGet}
	case RoutePrefix + "dispatches":
		return operatorRoute{kind: routeDispatches, method: http.MethodGet}
	}
	if path == RoutePrefix+"development-workspaces" ||
		strings.HasPrefix(path, RoutePrefix+"development-workspaces/") {
		return operatorRoute{kind: routePRWorkspaces}
	}
	if strings.HasPrefix(path, RoutePrefix+"dispatches/") {
		segments := strings.Split(
			strings.TrimPrefix(path, RoutePrefix+"dispatches/"),
			"/",
		)
		if len(segments) == 1 && segments[0] != "" {
			return operatorRoute{
				kind:       routeDispatch,
				dispatchID: segments[0],
				method:     http.MethodGet,
			}
		}
		return operatorRoute{}
	}
	if !strings.HasPrefix(path, RoutePrefix+"events/") {
		return operatorRoute{}
	}
	segments := strings.Split(strings.TrimPrefix(path, RoutePrefix+"events/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return operatorRoute{}
	}
	switch {
	case len(segments) == 1:
		return operatorRoute{
			kind:    routeEvent,
			eventID: segments[0],
			method:  http.MethodGet,
		}
	case len(segments) == 2 && segments[1] == "payload":
		return operatorRoute{
			kind:    routeEventPayload,
			eventID: segments[0],
			method:  http.MethodGet,
		}
	case len(segments) == 2 && segments[1] == "workflow-context":
		return operatorRoute{
			kind:    routeEventWorkflowContext,
			eventID: segments[0],
			method:  http.MethodGet,
		}
	case len(segments) == 2 && segments[1] == "replay":
		return operatorRoute{
			kind:    routeReplay,
			eventID: segments[0],
			method:  http.MethodPost,
		}
	default:
		return operatorRoute{}
	}
}

func eventListRequestFromQuery(rawQuery string) (EventListRequest, error) {
	values, err := strictQuery(rawQuery, map[string]struct{}{
		"connector":      {},
		"cursor":         {},
		"limit":          {},
		"routing_status": {},
		"source":         {},
		"type":           {},
	})
	if err != nil {
		return EventListRequest{}, err
	}
	limit, err := queryLimit(values)
	if err != nil {
		return EventListRequest{}, err
	}
	return EventListRequest{
		Source:        values.Get("source"),
		Connector:     values.Get("connector"),
		Type:          values.Get("type"),
		RoutingStatus: eventing.RoutingStatus(values.Get("routing_status")),
		Limit:         limit,
		Cursor:        values.Get("cursor"),
	}, nil
}

func dispatchListRequestFromQuery(rawQuery string) (DispatchListRequest, error) {
	values, err := strictQuery(rawQuery, map[string]struct{}{
		"cursor":       {},
		"event_id":     {},
		"limit":        {},
		"status":       {},
		"workflow_ref": {},
	})
	if err != nil {
		return DispatchListRequest{}, err
	}
	limit, err := queryLimit(values)
	if err != nil {
		return DispatchListRequest{}, err
	}
	return DispatchListRequest{
		EventID:     values.Get("event_id"),
		WorkflowRef: values.Get("workflow_ref"),
		Status:      eventing.DispatchStatus(values.Get("status")),
		Limit:       limit,
		Cursor:      values.Get("cursor"),
	}, nil
}

func strictQuery(
	rawQuery string,
	allowed map[string]struct{},
) (url.Values, error) {
	if len(rawQuery) > maxQueryBytes {
		return nil, invalidQuery()
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, invalidQuery()
	}
	for name, candidates := range values {
		if _, ok := allowed[name]; !ok ||
			len(candidates) != 1 ||
			candidates[0] == "" {
			return nil, invalidQuery()
		}
	}
	return values, nil
}

func queryLimit(values url.Values) (int, error) {
	raw := values.Get("limit")
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(limit) != raw {
		return 0, invalidQuery()
	}
	return limit, nil
}

func invalidQuery() error {
	return fmt.Errorf("%w: query is invalid", ErrInvalidRequest)
}

func emptyReplayObject(w http.ResponseWriter, request *http.Request) bool {
	if request.Body == nil ||
		request.ContentLength > maxReplayBodyBytes {
		return false
	}
	request.Body = http.MaxBytesReader(w, request.Body, maxReplayBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') || decoder.More() {
		return false
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return false
	}
	var trailing json.RawMessage
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func jsonRequestContentType(header http.Header) bool {
	value, ok := exactlyOneHeader(header, "Content-Type")
	if !ok {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	for name, parameter := range parameters {
		if !strings.EqualFold(name, "charset") ||
			!strings.EqualFold(parameter, "utf-8") {
			return false
		}
	}
	return true
}

func identityRequestEncoding(header http.Header) bool {
	var values []string
	for name, candidates := range header {
		if strings.EqualFold(name, "Content-Encoding") {
			values = append(values, candidates...)
		}
	}
	return len(values) == 0 ||
		len(values) == 1 &&
			strings.EqualFold(strings.TrimSpace(values[0]), "identity")
}

func exactlyOneHeader(header http.Header, target string) (string, bool) {
	var values []string
	for name, candidates := range header {
		if strings.EqualFold(name, target) {
			values = append(values, candidates...)
		}
	}
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func writeOperatorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeOperatorStatus(w, http.StatusBadRequest)
	case errors.Is(err, eventing.ErrNotFound):
		writeOperatorStatus(w, http.StatusNotFound)
	default:
		w.Header().Set("Retry-After", "1")
		writeOperatorStatus(w, http.StatusServiceUnavailable)
	}
}

func writeReplayOperatorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeOperatorStatus(w, http.StatusBadRequest)
	case errors.Is(err, eventing.ErrNotFound):
		writeOperatorStatus(w, http.StatusNotFound)
	default:
		writeOperatorMessage(
			w,
			http.StatusInternalServerError,
			replayUnknownOutcomeMessage,
		)
	}
}

func writeOperatorStatus(w http.ResponseWriter, status int) {
	writeOperatorMessage(w, status, http.StatusText(status))
}

func writeOperatorMessage(w http.ResponseWriter, status int, message string) {
	writeOperatorJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func writeOperatorJSON(w http.ResponseWriter, status int, value any) {
	setOperatorResponseHeaders(w)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeWorkflowEventJSON(w http.ResponseWriter, status int, value WorkflowEventView) {
	setOperatorResponseHeaders(w)
	w.Header().Set(WorkflowEventPayloadBytesHeader, strconv.Itoa(len(value.Payload)))
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	// Payload is an already-redacted RawMessage. Disabling HTML escaping keeps
	// its encoded size within the configured ingress maximum and preserves
	// literal <, >, and & bytes through the protected internal hop.
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writePayload(w http.ResponseWriter, payload []byte) {
	setOperatorResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func setOperatorResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
