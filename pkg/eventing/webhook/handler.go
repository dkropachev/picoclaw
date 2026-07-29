package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	maxWebhookIDBytes        = 1024
	maxWebhookTimestampBytes = 32
	maxWebhookSignatureBytes = 8192
)

type admissionRequest struct {
	eventType  string
	actor      *eventing.Actor
	subject    *eventing.Subject
	occurredAt *time.Time
	payload    json.RawMessage
	attributes map[string]string
}

type admissionResponse struct {
	EventID  string `json:"event_id"`
	Inserted bool   `json:"inserted"`
}

type admissionAuthentication struct {
	dedupeKey    string
	githubEvent  string
	githubDigest []byte
}

func (backend *Backend) serveHTTP(w http.ResponseWriter, request *http.Request) {
	connector, _ := connectorFromPath(request.URL.Path)
	runtime, exists := backend.connectors[connector]
	if !exists {
		writeError(w, http.StatusNotFound)
		return
	}

	authentication, headersOK := runtime.authenticationHeaders(request.Header)
	if !headersOK {
		writeError(w, http.StatusUnauthorized)
		return
	}
	if !identityEncoding(request.Header) || !jsonContentType(request.Header) {
		writeError(w, http.StatusUnsupportedMediaType)
		return
	}

	request.Body = http.MaxBytesReader(w, request.Body, runtime.maxRequestBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var maximumError *http.MaxBytesError
		if errors.As(err, &maximumError) {
			writeError(w, http.StatusRequestEntityTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest)
		return
	}

	if !runtime.verify(body, request.Header, authentication) {
		writeError(w, http.StatusUnauthorized)
		return
	}

	input, source, decodeErr := runtime.decode(body, authentication)
	if decodeErr != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	if backend.identityContainsSecret(authentication.dedupeKey, input.eventType) {
		writeError(w, http.StatusBadRequest)
		return
	}

	result, err := backend.store.Insert(request.Context(), eventing.Envelope{
		Source:     source,
		Connector:  connector,
		Type:       input.eventType,
		DedupeKey:  authentication.dedupeKey,
		Actor:      input.actor,
		Subject:    input.subject,
		OccurredAt: input.occurredAt,
		Payload:    input.payload,
		Attributes: input.attributes,
	})
	if err != nil {
		switch {
		case errors.Is(err, eventing.ErrPayloadTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge)
		case errors.Is(err, eventing.ErrInvalidEnvelope),
			errors.Is(err, eventing.ErrTimestampOutOfRange):
			writeError(w, http.StatusBadRequest)
		default:
			writeRetryableError(w)
		}
		return
	}

	status := http.StatusOK
	if result.Inserted {
		status = http.StatusAccepted
	}
	writeJSON(w, status, admissionResponse{
		EventID:  result.Event.Envelope.ID,
		Inserted: result.Inserted,
	})
}

func (runtime connectorRuntime) authenticationHeaders(
	headers http.Header,
) (admissionAuthentication, bool) {
	switch runtime.format {
	case connectorFormatStandard:
		id, ok := standardAuthenticationHeaders(headers)
		return admissionAuthentication{dedupeKey: id}, ok
	case connectorFormatGitHub:
		return githubAuthenticationHeaders(headers)
	default:
		return admissionAuthentication{}, false
	}
}

func (runtime connectorRuntime) verify(
	body []byte,
	headers http.Header,
	authentication admissionAuthentication,
) bool {
	switch runtime.format {
	case connectorFormatStandard:
		return runtime.standardVerifier != nil &&
			runtime.standardVerifier.Verify(body, headers) == nil
	case connectorFormatGitHub:
		return verifyGitHubSignature(
			runtime.githubSecret,
			body,
			authentication.githubDigest,
		)
	default:
		return false
	}
}

func (runtime connectorRuntime) decode(
	body []byte,
	authentication admissionAuthentication,
) (admissionRequest, string, error) {
	switch runtime.format {
	case connectorFormatStandard:
		input, err := decodeAdmissionRequest(body)
		return input, "webhook", err
	case connectorFormatGitHub:
		input, err := decodeGitHubAdmissionRequest(body, authentication.githubEvent)
		return input, "github", err
	default:
		return admissionRequest{}, "", errors.New("unsupported webhook connector format")
	}
}

func (backend *Backend) identityContainsSecret(values ...string) bool {
	for _, value := range values {
		for _, secret := range backend.secretValues {
			if strings.Contains(value, secret) {
				return true
			}
		}
	}
	return false
}

func standardAuthenticationHeaders(headers http.Header) (string, bool) {
	id, idOK := exactlyOneHeader(headers, standardwebhooks.HeaderWebhookID)
	timestamp, timestampOK := exactlyOneHeader(
		headers,
		standardwebhooks.HeaderWebhookTimestamp,
	)
	signature, signatureOK := exactlyOneHeader(
		headers,
		standardwebhooks.HeaderWebhookSignature,
	)
	if !idOK || !timestampOK || !signatureOK ||
		len(id) > maxWebhookIDBytes ||
		len(timestamp) > maxWebhookTimestampBytes ||
		len(signature) > maxWebhookSignatureBytes ||
		strings.TrimSpace(id) != id {
		return "", false
	}
	return id, true
}

func exactlyOneHeader(headers http.Header, target string) (string, bool) {
	var values []string
	for name, candidates := range headers {
		if strings.EqualFold(name, target) {
			values = append(values, candidates...)
		}
	}
	return first(values), len(values) == 1 && values[0] != ""
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func identityEncoding(headers http.Header) bool {
	var values []string
	for name, candidates := range headers {
		if strings.EqualFold(name, "Content-Encoding") {
			values = append(values, candidates...)
		}
	}
	return len(values) == 0 ||
		len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "identity")
}

func jsonContentType(headers http.Header) bool {
	value, ok := exactlyOneHeader(headers, "Content-Type")
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

func decodeAdmissionRequest(body []byte) (admissionRequest, error) {
	if !utf8.Valid(body) {
		return admissionRequest{}, errors.New("request body is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return admissionRequest{}, errors.New("request body must be a JSON object")
	}

	var input admissionRequest
	seen := make(map[string]struct{}, 6)
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, nameOK := nameToken.(string)
		if tokenErr != nil || !nameOK {
			return admissionRequest{}, errors.New("request body has an invalid field name")
		}
		if _, duplicate := seen[name]; duplicate {
			return admissionRequest{}, errors.New("request body has a duplicate field")
		}
		seen[name] = struct{}{}

		var raw json.RawMessage
		if decodeErr := decoder.Decode(&raw); decodeErr != nil {
			return admissionRequest{}, errors.New("request body has an invalid field value")
		}
		switch name {
		case "type":
			input.eventType, err = decodeString(raw)
		case "occurred_at":
			input.occurredAt, err = decodeTimestamp(raw)
		case "actor":
			input.actor, err = decodeActor(raw)
		case "subject":
			input.subject, err = decodeSubject(raw)
		case "attributes":
			input.attributes, err = decodeStringMap(raw)
		case "payload":
			input.payload, err = decodePayload(raw)
		default:
			return admissionRequest{}, errors.New("request body has an unknown field")
		}
		if err != nil {
			return admissionRequest{}, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return admissionRequest{}, errors.New("request body is not a complete JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return admissionRequest{}, errors.New("request body contains trailing data")
	}
	if _, exists := seen["type"]; !exists || strings.TrimSpace(input.eventType) == "" {
		return admissionRequest{}, errors.New("request type is required")
	}
	if _, exists := seen["payload"]; !exists {
		return admissionRequest{}, errors.New("request payload is required")
	}
	return input, nil
}

func decodeString(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '"' {
		return "", errors.New("field must be a JSON string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("field must be a JSON string")
	}
	return value, nil
}

func decodeTimestamp(raw json.RawMessage) (*time.Time, error) {
	value, err := decodeString(raw)
	if err != nil {
		return nil, errors.New("occurred_at must be an RFC3339 string")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, errors.New("occurred_at must be an RFC3339 string")
	}
	return &timestamp, nil
}

func decodeActor(raw json.RawMessage) (*eventing.Actor, error) {
	fields, err := decodeObject(raw, map[string]struct{}{
		"id": {}, "type": {}, "display_name": {}, "attributes": {},
	})
	if err != nil {
		return nil, errors.New("actor must be a canonical actor object")
	}
	actor := &eventing.Actor{}
	if value, exists := fields["id"]; exists {
		actor.ID, err = decodeString(value)
	}
	if err == nil {
		if value, exists := fields["type"]; exists {
			actor.Type, err = decodeString(value)
		}
	}
	if err == nil {
		if value, exists := fields["display_name"]; exists {
			actor.DisplayName, err = decodeString(value)
		}
	}
	if err == nil {
		if value, exists := fields["attributes"]; exists {
			actor.Attributes, err = decodeStringMap(value)
		}
	}
	if err != nil {
		return nil, errors.New("actor must be a canonical actor object")
	}
	return actor, nil
}

func decodeSubject(raw json.RawMessage) (*eventing.Subject, error) {
	fields, err := decodeObject(raw, map[string]struct{}{
		"id": {}, "type": {}, "name": {}, "url": {}, "attributes": {},
	})
	if err != nil {
		return nil, errors.New("subject must be a canonical subject object")
	}
	subject := &eventing.Subject{}
	if value, exists := fields["id"]; exists {
		subject.ID, err = decodeString(value)
	}
	if err == nil {
		if value, exists := fields["type"]; exists {
			subject.Type, err = decodeString(value)
		}
	}
	if err == nil {
		if value, exists := fields["name"]; exists {
			subject.Name, err = decodeString(value)
		}
	}
	if err == nil {
		if value, exists := fields["url"]; exists {
			subject.URL, err = decodeString(value)
		}
	}
	if err == nil {
		if value, exists := fields["attributes"]; exists {
			subject.Attributes, err = decodeStringMap(value)
		}
	}
	if err != nil {
		return nil, errors.New("subject must be a canonical subject object")
	}
	return subject, nil
}

func decodeObject(
	raw json.RawMessage,
	allowed map[string]struct{},
) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("field must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("field must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, ok := nameToken.(string)
		if tokenErr != nil || !ok {
			return nil, errors.New("object field name is invalid")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("object field is duplicated")
		}
		if _, exists := allowed[name]; !exists {
			return nil, errors.New("object field is unknown")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("object field value is invalid")
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("object is incomplete")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("object contains trailing data")
	}
	return fields, nil
}

func decodeStringMap(raw json.RawMessage) (map[string]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("field must be a string-valued JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("field must be a string-valued JSON object")
	}
	values := make(map[string]string)
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		key, ok := keyToken.(string)
		if tokenErr != nil || !ok {
			return nil, errors.New("map key is invalid")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, errors.New("map key is duplicated")
		}
		var valueRaw json.RawMessage
		if err := decoder.Decode(&valueRaw); err != nil {
			return nil, errors.New("map value is invalid")
		}
		value, err := decodeString(valueRaw)
		if err != nil {
			return nil, errors.New("map value must be a string")
		}
		values[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("map is incomplete")
	}
	return values, nil
}

func decodePayload(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("payload must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, errors.New("payload must be a JSON object")
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

func writeRetryableError(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusServiceUnavailable)
}

func writeError(w http.ResponseWriter, status int) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: http.StatusText(status)})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
