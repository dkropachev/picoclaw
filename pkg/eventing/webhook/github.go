package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	githubSignatureHeader = "X-Hub-Signature-256"
	githubEventHeader     = "X-Github-Event"
	githubDeliveryHeader  = "X-Github-Delivery"

	githubSignaturePrefix          = "sha256="
	maxGitHubEventHeaderBytes      = 128
	maxGitHubActionBytes           = 127
	maxGitHubEntityFieldBytes      = 1024
	maxGitHubAttributeValueBytes   = 2048
	maxGitHubNormalizedEventBytes  = 256
	githubSignatureAlgorithm       = "hmac-sha256"
	githubAuthenticatedBodyValue   = "true"
	githubUnauthenticatedHeadValue = "false"
)

type githubSenderPayload struct {
	Login   string          `json:"login"`
	ID      json.RawMessage `json:"id"`
	NodeID  string          `json:"node_id"`
	Type    string          `json:"type"`
	HTMLURL string          `json:"html_url"`
}

type githubRepositoryPayload struct {
	ID            json.RawMessage `json:"id"`
	NodeID        string          `json:"node_id"`
	Name          string          `json:"name"`
	FullName      string          `json:"full_name"`
	HTMLURL       string          `json:"html_url"`
	DefaultBranch string          `json:"default_branch"`
	Visibility    string          `json:"visibility"`
	Private       *bool           `json:"private"`
	Fork          *bool           `json:"fork"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func githubAuthenticationHeaders(
	headers http.Header,
) (admissionAuthentication, bool) {
	signature, signatureOK := exactlyOneHeader(headers, githubSignatureHeader)
	event, eventOK := exactlyOneHeader(headers, githubEventHeader)
	delivery, deliveryOK := exactlyOneHeader(headers, githubDeliveryHeader)
	digest, digestOK := canonicalGitHubDigest(signature)
	if !signatureOK ||
		!eventOK ||
		!deliveryOK ||
		!digestOK ||
		!validGitHubName(event, maxGitHubEventHeaderBytes) ||
		!validGitHubDelivery(delivery) {
		return admissionAuthentication{}, false
	}
	return admissionAuthentication{
		dedupeKey:    delivery,
		githubEvent:  event,
		githubDigest: digest,
	}, true
}

func canonicalGitHubDigest(signature string) ([]byte, bool) {
	if len(signature) != len(githubSignaturePrefix)+sha256.Size*2 ||
		!strings.HasPrefix(signature, githubSignaturePrefix) {
		return nil, false
	}
	encoded := strings.TrimPrefix(signature, githubSignaturePrefix)
	digest, err := hex.DecodeString(encoded)
	if err != nil || hex.EncodeToString(digest) != encoded {
		return nil, false
	}
	return digest, true
}

func verifyGitHubSignature(secret, body, digest []byte) bool {
	if len(secret) == 0 || len(digest) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), digest)
}

func decodeGitHubAdmissionRequest(
	body []byte,
	event string,
) (admissionRequest, error) {
	fields, payload, err := decodeGitHubObject(body)
	if err != nil {
		return admissionRequest{}, err
	}

	eventType := event
	if raw, exists := fields["action"]; exists {
		action, decodeErr := decodeString(raw)
		if decodeErr != nil ||
			!validGitHubName(action, maxGitHubActionBytes) {
			return admissionRequest{}, errors.New("GitHub action must be a canonical string")
		}
		eventType += "." + action
	}
	if len(eventType) > maxGitHubNormalizedEventBytes {
		return admissionRequest{}, errors.New("GitHub event type is too long")
	}

	actor := githubActor(fields["sender"])
	subject, repositoryAttributes := githubSubject(fields["repository"])
	attributes := githubAttributes(map[string]string{
		"body_authenticated":    githubAuthenticatedBodyValue,
		"headers_authenticated": githubUnauthenticatedHeadValue,
		"signature_algorithm":   githubSignatureAlgorithm,
		"repository_id":         repositoryAttributes["id"],
		"repository_full_name":  repositoryAttributes["full_name"],
		"repository_url":        repositoryAttributes["url"],
		"repository_owner":      repositoryAttributes["owner"],
		"repository_visibility": repositoryAttributes["visibility"],
		"repository_private":    repositoryAttributes["private"],
		"repository_branch":     repositoryAttributes["default_branch"],
	})

	return admissionRequest{
		eventType:  eventType,
		actor:      actor,
		subject:    subject,
		payload:    payload,
		attributes: attributes,
	}, nil
}

func decodeGitHubObject(
	body []byte,
) (map[string]json.RawMessage, json.RawMessage, error) {
	if !utf8.Valid(body) {
		return nil, nil, errors.New("GitHub payload is not valid UTF-8")
	}
	trimmed := bytes.TrimSpace(body)
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, nil, errors.New("GitHub payload must be a JSON object")
	}

	fields := make(map[string]json.RawMessage)
	seen := make(map[string]struct{})
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, nameOK := nameToken.(string)
		if tokenErr != nil || !nameOK {
			return nil, nil, errors.New("GitHub payload has an invalid field name")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, errors.New("GitHub payload has a duplicate field")
		}
		seen[name] = struct{}{}

		var raw json.RawMessage
		if decodeErr := decoder.Decode(&raw); decodeErr != nil {
			return nil, nil, errors.New("GitHub payload has an invalid field value")
		}
		switch name {
		case "action", "sender", "repository":
			fields[name] = raw
		}
	}
	if _, err = decoder.Token(); err != nil {
		return nil, nil, errors.New("GitHub payload is incomplete")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("GitHub payload contains trailing data")
	}
	return fields, append(json.RawMessage(nil), trimmed...), nil
}

func githubActor(raw json.RawMessage) *eventing.Actor {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var sender githubSenderPayload
	if err := json.Unmarshal(raw, &sender); err != nil {
		return nil
	}

	databaseID := githubDatabaseID(sender.ID)
	id := githubStableID(sender.NodeID)
	if id == "" {
		id = databaseID
	}
	actorType := boundedGitHubString(
		strings.ToLower(sender.Type),
		maxGitHubEntityFieldBytes,
	)
	login := boundedGitHubString(sender.Login, maxGitHubEntityFieldBytes)
	url := boundedGitHubString(sender.HTMLURL, maxGitHubAttributeValueBytes)
	if id == "" && actorType == "" && login == "" && url == "" {
		return nil
	}
	return &eventing.Actor{
		ID:          id,
		Type:        actorType,
		DisplayName: login,
		Attributes: githubAttributes(map[string]string{
			"database_id": databaseID,
			"login":       login,
			"url":         url,
		}),
	}
}

func githubSubject(
	raw json.RawMessage,
) (*eventing.Subject, map[string]string) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var repository githubRepositoryPayload
	if err := json.Unmarshal(raw, &repository); err != nil {
		return nil, nil
	}

	databaseID := githubDatabaseID(repository.ID)
	nodeID := githubStableID(repository.NodeID)
	id := nodeID
	if id == "" {
		id = databaseID
	}
	name := boundedGitHubString(repository.FullName, maxGitHubEntityFieldBytes)
	if name == "" {
		name = boundedGitHubString(repository.Name, maxGitHubEntityFieldBytes)
	}
	url := boundedGitHubString(repository.HTMLURL, maxGitHubEntityFieldBytes)
	repositoryAttributes := githubAttributes(map[string]string{
		"id":             id,
		"database_id":    databaseID,
		"node_id":        nodeID,
		"full_name":      name,
		"url":            url,
		"owner":          repository.Owner.Login,
		"default_branch": repository.DefaultBranch,
		"visibility":     repository.Visibility,
		"private":        githubBool(repository.Private),
		"fork":           githubBool(repository.Fork),
	})
	if len(repositoryAttributes) == 0 {
		return nil, nil
	}
	return &eventing.Subject{
		ID:   id,
		Type: "repository",
		Name: name,
		URL:  url,
		Attributes: githubAttributes(map[string]string{
			"database_id":    databaseID,
			"node_id":        nodeID,
			"owner":          repositoryAttributes["owner"],
			"default_branch": repositoryAttributes["default_branch"],
			"visibility":     repositoryAttributes["visibility"],
			"private":        repositoryAttributes["private"],
			"fork":           repositoryAttributes["fork"],
		}),
	}, repositoryAttributes
}

func githubDatabaseID(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || len(value) > maxGitHubEntityFieldBytes {
		return ""
	}
	for _, char := range []byte(value) {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return value
}

func githubBool(value *bool) string {
	switch {
	case value == nil:
		return ""
	case *value:
		return "true"
	default:
		return "false"
	}
}

func githubAttributes(values map[string]string) map[string]string {
	attributes := make(map[string]string, len(values))
	for key, value := range values {
		value = boundedGitHubString(value, maxGitHubAttributeValueBytes)
		if value != "" {
			attributes[key] = value
		}
	}
	if len(attributes) == 0 {
		return nil
	}
	return attributes
}

func boundedGitHubString(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func validGitHubName(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, char := range []byte(value) {
		if char >= 'a' && char <= 'z' ||
			char >= '0' && char <= '9' ||
			char == '_' {
			continue
		}
		return false
	}
	return true
}

func validGitHubDelivery(value string) bool {
	if value == "" ||
		len(value) > maxWebhookIDBytes ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range []byte(value) {
		if char < '!' || char > '~' || char == ',' {
			return false
		}
	}
	return true
}

func githubStableID(value string) string {
	if value == "" ||
		len(value) > maxGitHubEntityFieldBytes ||
		strings.TrimSpace(value) != value {
		return ""
	}
	return value
}
