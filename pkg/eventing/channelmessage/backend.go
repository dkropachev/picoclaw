// Package channelmessage mirrors normalized channel messages into the durable
// event inbox without exposing channel-internal routing or reply state.
package channelmessage

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	EventTypeMessageReceived = "message.received"

	fallbackPayload              = `{"truncated":true}`
	maxSafeEntityBytes           = 2048
	maxSafeAttributeValueBytes   = 8192
	maxSafeAttachmentStringBytes = 2048
	maxSafeAttachmentCount       = 128
	deltaChatChannelType         = "deltachat"
)

// Source identifies the kind of channel adapter producing an event.
type Source string

const (
	SourceChat  Source = "chat"
	SourceEmail Source = "email"
)

// Mode controls whether a durably admitted message also continues through the
// ordinary conversational runtime.
type Mode string

const (
	ModeMirror    Mode = "mirror"
	ModeEventOnly Mode = "event_only"
)

// ErrStableIdentityRequired prevents configured adapters from admitting a
// message that cannot be deduplicated across reconnects and restarts.
var ErrStableIdentityRequired = errors.New("channel event admission requires a stable message identity")

// Inserter is the synchronous durable boundary required by channel mirroring.
type Inserter interface {
	Insert(
		ctx context.Context,
		event eventing.Envelope,
	) (eventing.InsertResult, error)
}

// AdapterConfig describes one enabled channel connector.
type AdapterConfig struct {
	Source               Source
	Mode                 Mode
	ChannelType          string
	AllowUnverifiedEmail bool
}

// BackendConfig contains the immutable dependencies for one gateway
// generation.
type BackendConfig struct {
	Store           Inserter
	Adapters        map[string]AdapterConfig
	MaxPayloadBytes int
}

// Backend is an immutable, prevalidated channel admission backend.
type Backend struct {
	store           Inserter
	adapters        map[string]AdapterConfig
	maxPayloadBytes int
}

// NewBackend validates and copies a candidate backend configuration.
func NewBackend(config BackendConfig) (*Backend, error) {
	if config.Store == nil {
		return nil, errors.New("channel event admission store is required")
	}
	if config.MaxPayloadBytes < len(fallbackPayload) {
		return nil, errors.New("channel event admission maximum payload bytes is invalid")
	}
	if len(config.Adapters) == 0 {
		return nil, errors.New("channel event admission requires at least one adapter")
	}

	adapters := make(map[string]AdapterConfig, len(config.Adapters))
	for connector, adapter := range config.Adapters {
		if !validConnector(connector) {
			return nil, fmt.Errorf("channel event adapter connector %q is invalid", connector)
		}
		if adapter.Source != SourceChat && adapter.Source != SourceEmail {
			return nil, fmt.Errorf("channel event adapter %q has an invalid source", connector)
		}
		if adapter.Mode != ModeMirror && adapter.Mode != ModeEventOnly {
			return nil, fmt.Errorf("channel event adapter %q has an invalid mode", connector)
		}
		if !validChannelType(adapter.ChannelType) {
			return nil, fmt.Errorf("channel event adapter %q has an invalid channel type", connector)
		}
		if adapter.ChannelType == deltaChatChannelType &&
			adapter.Source != SourceEmail {
			return nil, fmt.Errorf("channel event adapter %q has an invalid Delta Chat source", connector)
		}
		if adapter.Source == SourceEmail &&
			adapter.ChannelType != deltaChatChannelType {
			return nil, fmt.Errorf("channel event adapter %q has an invalid email channel type", connector)
		}
		if adapter.AllowUnverifiedEmail && adapter.Source != SourceEmail {
			return nil, fmt.Errorf("channel event adapter %q has an invalid unverified-email policy", connector)
		}
		adapters[connector] = AdapterConfig{
			Source:               adapter.Source,
			Mode:                 adapter.Mode,
			ChannelType:          adapter.ChannelType,
			AllowUnverifiedEmail: adapter.AllowUnverifiedEmail,
		}
	}

	return &Backend{
		store:           config.Store,
		adapters:        adapters,
		maxPayloadBytes: config.MaxPayloadBytes,
	}, nil
}

// ConnectorCount reports the number of configured channel connectors.
func (backend *Backend) ConnectorCount() int {
	if backend == nil {
		return 0
	}
	return len(backend.adapters)
}

// ConnectorNames returns a stable copy of the configured connector identities.
func (backend *Backend) ConnectorNames() []string {
	if backend == nil {
		return nil
	}
	names := make([]string, 0, len(backend.adapters))
	for connector := range backend.adapters {
		names = append(names, connector)
	}
	sort.Strings(names)
	return names
}

// HasConnector reports whether connector belongs to this backend generation.
func (backend *Backend) HasConnector(connector string) bool {
	if backend == nil {
		return false
	}
	_, ok := backend.adapters[connector]
	return ok
}

// AdmitInbound synchronously mirrors a configured channel-originated message
// to the durable inbox. Unconfigured and internal messages pass through
// untouched. A configured message is never forwarded unless its durable insert
// succeeds (or resolves to an existing deduplicated event).
func (backend *Backend) AdmitInbound(
	ctx context.Context,
	message bus.InboundMessage,
) (bool, error) {
	if !message.ChannelOrigin {
		return true, nil
	}

	connector := messageConnector(message)
	adapter, configured := backend.adapters[connector]
	if !configured {
		return true, nil
	}
	if adapter.Source == SourceEmail &&
		!adapter.AllowUnverifiedEmail &&
		(!message.EventSenderVerified ||
			!message.EventTransportAuthenticated) {
		logger.WarnCF("eventing", "Skipped unverified channel email event", map[string]any{
			"connector": connector,
		})
		return adapter.Mode == ModeMirror, nil
	}

	stableID := strings.TrimSpace(message.EventDedupeID)
	if stableID == "" {
		stableID = strings.TrimSpace(message.Context.MessageID)
	}
	if stableID == "" {
		return false, ErrStableIdentityRequired
	}

	payload, err := buildPayload(message, backend.maxPayloadBytes)
	if err != nil {
		return false, err
	}

	event := eventing.Envelope{
		Source:     string(adapter.Source),
		Connector:  connector,
		Type:       EventTypeMessageReceived,
		DedupeKey:  messageDedupeKey(message.Context, stableID),
		Actor:      actorFromMessage(message, adapter),
		Subject:    subjectFromMessage(message),
		OccurredAt: cloneTime(message.OccurredAt),
		Payload:    payload,
		Attributes: compactAttributes(map[string]string{
			"channel_type": adapter.ChannelType,
			"email_trust":  emailTrust(message, adapter),
		}),
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err = backend.store.Insert(ctx, event); err != nil {
		return false, fmt.Errorf("insert channel event: %w", err)
	}
	return adapter.Mode == ModeMirror, nil
}

func emailTrust(message bus.InboundMessage, adapter AdapterConfig) string {
	if adapter.Source != SourceEmail {
		return ""
	}
	if message.EventSenderVerified &&
		message.EventTransportAuthenticated {
		return "verified"
	}
	return "unverified"
}

func messageConnector(message bus.InboundMessage) string {
	if message.Context.Channel != "" {
		return message.Context.Channel
	}
	return message.Channel
}

func messageDedupeKey(ctx bus.InboundContext, stableID string) string {
	hash := sha256.New()
	writeLengthPrefixed(hash, strings.TrimSpace(ctx.Account))
	writeLengthPrefixed(hash, strings.TrimSpace(ctx.ChatID))
	writeLengthPrefixed(hash, strings.TrimSpace(ctx.TopicID))
	writeLengthPrefixed(hash, stableID)
	return hex.EncodeToString(hash.Sum(nil))
}

type byteWriter interface {
	Write(data []byte) (written int, err error)
}

func writeLengthPrefixed(writer byteWriter, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}

type inboundPayload struct {
	Text             string              `json:"text,omitempty"`
	Subject          string              `json:"subject,omitempty"`
	MessageID        string              `json:"message_id"`
	ReplyToMessageID string              `json:"reply_to_message_id,omitempty"`
	ReplyToSenderID  string              `json:"reply_to_sender_id,omitempty"`
	Attachments      []attachmentPayload `json:"attachments,omitempty"`
	Truncated        bool                `json:"truncated,omitempty"`
}

type attachmentPayload struct {
	Kind        string `json:"kind,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
}

func buildPayload(
	message bus.InboundMessage,
	maxBytes int,
) (json.RawMessage, error) {
	text, textTruncated := boundedText(message.Content, maxBytes)
	attachments, attachmentsTruncated := safeAttachments(
		message.Attachments,
		maxBytes,
	)
	payload := inboundPayload{
		Text:             text,
		Subject:          safeString(strings.TrimSpace(message.EventSubject), maxSafeEntityBytes),
		MessageID:        safeString(message.Context.MessageID, maxSafeAttachmentStringBytes),
		ReplyToMessageID: safeString(message.Context.ReplyToMessageID, maxSafeAttachmentStringBytes),
		ReplyToSenderID:  safeString(message.Context.ReplyToSenderID, maxSafeAttachmentStringBytes),
		Attachments:      attachments,
		Truncated:        textTruncated || attachmentsTruncated,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode channel event payload: %w", err)
	}
	if len(encoded) <= maxBytes {
		return encoded, nil
	}

	payload.Truncated = true
	payload.Text = ""
	withoutText, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode truncated channel event payload: %w", err)
	}
	if len(withoutText) > maxBytes {
		return json.RawMessage(fallbackPayload), nil
	}

	boundaries := runeBoundaries(text)
	low, high := 0, len(boundaries)-1
	best := withoutText
	for low <= high {
		mid := low + (high-low)/2
		payload.Text = text[:boundaries[mid]]
		candidate, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode truncated channel event payload: %w", marshalErr)
		}
		if len(candidate) <= maxBytes {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best, nil
}

func boundedText(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return strings.ToValidUTF8(value, "\uFFFD"), false
	}
	end := validPrefixEnd(value, maxBytes)
	return strings.ToValidUTF8(value[:end], "\uFFFD"), true
}

func runeBoundaries(value string) []int {
	boundaries := make([]int, 0, utf8.RuneCountInString(value)+1)
	for index := range value {
		boundaries = append(boundaries, index)
	}
	return append(boundaries, len(value))
}

func safeAttachments(
	attachments []bus.InboundAttachment,
	maxWorkBytes int,
) ([]attachmentPayload, bool) {
	if len(attachments) == 0 {
		return nil, false
	}
	capacity := min(len(attachments), maxSafeAttachmentCount, maxWorkBytes)
	safe := make([]attachmentPayload, 0, capacity)
	remaining := maxWorkBytes
	truncated := false
	for _, attachment := range attachments {
		if len(safe) >= maxSafeAttachmentCount {
			truncated = true
			break
		}
		// Charge one unit even for an all-empty attachment so an attacker cannot
		// force unbounded iteration with a large slice of zero-value metadata.
		if remaining <= 0 {
			truncated = true
			break
		}
		remaining--

		kind, used, kindTruncated := safeStringWithinBudget(
			attachment.Kind,
			remaining,
		)
		remaining -= used
		filename, used, filenameTruncated := safeStringWithinBudget(
			attachment.Filename,
			remaining,
		)
		remaining -= used
		contentType, used, contentTypeTruncated := safeStringWithinBudget(
			attachment.ContentType,
			remaining,
		)
		remaining -= used
		truncated = truncated ||
			kindTruncated ||
			filenameTruncated ||
			contentTypeTruncated

		size := attachment.SizeBytes
		if size < 0 {
			size = 0
		}
		safe = append(safe, attachmentPayload{
			Kind:        kind,
			Filename:    filename,
			ContentType: contentType,
			SizeBytes:   size,
		})
	}
	if len(safe) != len(attachments) {
		truncated = true
	}
	return safe, truncated
}

func safeStringWithinBudget(value string, remaining int) (string, int, bool) {
	limit := min(maxSafeAttachmentStringBytes, remaining)
	if limit <= 0 {
		return "", 0, value != ""
	}
	inspected := min(len(value), limit)
	end := validPrefixEnd(value, inspected)
	safe := strings.ToValidUTF8(value[:end], "\uFFFD")
	if len(safe) > limit {
		safe = safe[:validPrefixEnd(safe, limit)]
	}
	return safe, inspected, len(value) > inspected
}

func actorFromMessage(
	message bus.InboundMessage,
	adapter AdapterConfig,
) *eventing.Actor {
	platform := strings.TrimSpace(message.Sender.Platform)
	platformID := strings.TrimSpace(message.Sender.PlatformID)
	if platformID == "" {
		platformID = strings.TrimSpace(message.Context.SenderID)
	}
	canonicalID := strings.TrimSpace(message.Sender.CanonicalID)
	if canonicalID == "" && platformID != "" {
		if platform == "" {
			platform = strings.TrimSpace(adapter.ChannelType)
		}
		canonicalID = platform + ":" + platformID
	}

	displayName := strings.TrimSpace(message.Sender.DisplayName)
	username := strings.TrimSpace(message.Sender.Username)
	if canonicalID == "" && displayName == "" && username == "" && platform == "" {
		return nil
	}
	actorType := "user"
	if adapter.Source == SourceEmail {
		actorType = "email_address"
	}
	return &eventing.Actor{
		ID:          safeString(canonicalID, maxSafeEntityBytes),
		Type:        actorType,
		DisplayName: safeString(displayName, maxSafeEntityBytes),
		Attributes: compactAttributes(map[string]string{
			"platform":                platform,
			"username":                username,
			"sender_verified":         emailTrustBoolean(message.EventSenderVerified, adapter),
			"transport_authenticated": emailTrustBoolean(message.EventTransportAuthenticated, adapter),
		}),
	}
}

func emailTrustBoolean(value bool, adapter AdapterConfig) string {
	if adapter.Source != SourceEmail {
		return ""
	}
	if value {
		return "true"
	}
	return "false"
}

func subjectFromMessage(message bus.InboundMessage) *eventing.Subject {
	ctx := message.Context
	name := strings.TrimSpace(message.ConversationName)
	if ctx.ChatID == "" && name == "" {
		return nil
	}
	mentioned := ""
	if ctx.Mentioned {
		mentioned = "true"
	}
	return &eventing.Subject{
		ID:   safeString(strings.TrimSpace(ctx.ChatID), maxSafeEntityBytes),
		Type: "conversation",
		Name: safeString(name, maxSafeEntityBytes),
		Attributes: compactAttributes(map[string]string{
			"account":    strings.TrimSpace(ctx.Account),
			"chat_type":  strings.TrimSpace(ctx.ChatType),
			"topic_id":   strings.TrimSpace(ctx.TopicID),
			"space_id":   strings.TrimSpace(ctx.SpaceID),
			"space_type": strings.TrimSpace(ctx.SpaceType),
			"mentioned":  mentioned,
		}),
	}
}

func compactAttributes(values map[string]string) map[string]string {
	attributes := make(map[string]string, len(values))
	for key, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			attributes[key] = safeString(value, maxSafeAttributeValueBytes)
		}
	}
	if len(attributes) == 0 {
		return nil
	}
	return attributes
}

func safeString(value string, maxBytes int) string {
	if len(value) > maxBytes {
		value = value[:validPrefixEnd(value, maxBytes)]
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) > maxBytes {
		value = value[:validPrefixEnd(value, maxBytes)]
	}
	return value
}

func validPrefixEnd(value string, maximum int) int {
	end := min(len(value), maximum)
	for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return end
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validConnector(connector string) bool {
	return connector != "" &&
		connector == strings.TrimSpace(connector) &&
		utf8.ValidString(connector) &&
		len(connector) <= 256
}

func validChannelType(channelType string) bool {
	return channelType != "" &&
		channelType == strings.TrimSpace(channelType) &&
		utf8.ValidString(channelType) &&
		len(channelType) <= 128
}
