package bus

import (
	"strings"
	"time"
)

// NormalizeInboundMessage ensures the inbound context is normalized and keeps
// convenience mirrors in sync for runtime consumers.
func NormalizeInboundMessage(msg InboundMessage) InboundMessage {
	if msg.Context.Channel == "" {
		msg.Context.Channel = msg.Channel
	}
	if msg.Context.ChatID == "" {
		msg.Context.ChatID = msg.ChatID
	}
	if msg.Context.SenderID == "" {
		msg.Context.SenderID = msg.SenderID
	}
	if msg.Context.MessageID == "" {
		msg.Context.MessageID = msg.MessageID
	}
	if msg.Context.EventDedupeID == "" {
		msg.Context.EventDedupeID = msg.EventDedupeID
	}
	if msg.Context.OccurredAt == nil {
		msg.Context.OccurredAt = msg.OccurredAt
	}
	if msg.Context.EventSubject == "" {
		msg.Context.EventSubject = msg.EventSubject
	}
	if msg.Context.ConversationName == "" {
		msg.Context.ConversationName = msg.ConversationName
	}
	if len(msg.Context.Attachments) == 0 {
		msg.Context.Attachments = msg.Attachments
	}
	if !msg.Context.EventSenderVerified {
		msg.Context.EventSenderVerified = msg.EventSenderVerified
	}
	if !msg.Context.EventTransportAuthenticated {
		msg.Context.EventTransportAuthenticated = msg.EventTransportAuthenticated
	}
	msg.Context = normalizeInboundContext(msg.Context)
	msg.Channel = msg.Context.Channel
	msg.SenderID = msg.Context.SenderID
	msg.ChatID = msg.Context.ChatID
	if msg.MessageID == "" {
		msg.MessageID = msg.Context.MessageID
	}
	if msg.Context.MessageID == "" {
		msg.Context.MessageID = msg.MessageID
	}
	msg.Media = cloneStrings(msg.Media)
	msg.EventDedupeID = msg.Context.EventDedupeID
	msg.OccurredAt = cloneTime(msg.Context.OccurredAt)
	msg.EventSubject = msg.Context.EventSubject
	msg.ConversationName = msg.Context.ConversationName
	msg.Attachments = cloneInboundAttachments(msg.Context.Attachments)
	msg.EventSenderVerified = msg.Context.EventSenderVerified
	msg.EventTransportAuthenticated = msg.Context.EventTransportAuthenticated
	return msg
}

func (ctx InboundContext) isZero() bool {
	return ctx.Channel == "" &&
		ctx.Account == "" &&
		ctx.ChatID == "" &&
		ctx.ChatType == "" &&
		ctx.TopicID == "" &&
		ctx.SpaceID == "" &&
		ctx.SpaceType == "" &&
		ctx.SenderID == "" &&
		ctx.MessageID == "" &&
		!ctx.Mentioned &&
		ctx.ReplyToMessageID == "" &&
		ctx.ReplyToSenderID == "" &&
		len(ctx.ReplyHandles) == 0 &&
		len(ctx.Raw) == 0
}

func normalizeInboundContext(ctx InboundContext) InboundContext {
	ctx.Channel = strings.TrimSpace(ctx.Channel)
	ctx.Account = strings.TrimSpace(ctx.Account)
	ctx.ChatID = strings.TrimSpace(ctx.ChatID)
	ctx.ChatType = normalizeKind(ctx.ChatType)
	ctx.TopicID = strings.TrimSpace(ctx.TopicID)
	ctx.SpaceID = strings.TrimSpace(ctx.SpaceID)
	ctx.SpaceType = normalizeKind(ctx.SpaceType)
	ctx.SenderID = strings.TrimSpace(ctx.SenderID)
	ctx.MessageID = strings.TrimSpace(ctx.MessageID)
	ctx.ReplyToMessageID = strings.TrimSpace(ctx.ReplyToMessageID)
	ctx.ReplyToSenderID = strings.TrimSpace(ctx.ReplyToSenderID)
	ctx.ReplyHandles = cloneStringMap(ctx.ReplyHandles)
	ctx.Raw = cloneStringMap(ctx.Raw)
	ctx.TurnUXID = strings.TrimSpace(ctx.TurnUXID)
	ctx.EventDedupeID = strings.TrimSpace(ctx.EventDedupeID)
	ctx.OccurredAt = normalizeTime(ctx.OccurredAt)
	ctx.EventSubject = strings.TrimSpace(ctx.EventSubject)
	ctx.ConversationName = strings.TrimSpace(ctx.ConversationName)
	ctx.Attachments = cloneInboundAttachments(ctx.Attachments)
	return ctx
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneStrings(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	return append([]string(nil), src...)
}

func cloneInboundAttachments(src []InboundAttachment) []InboundAttachment {
	if len(src) == 0 {
		return nil
	}
	return append([]InboundAttachment(nil), src...)
}

func normalizeTime(src *time.Time) *time.Time {
	if src == nil || src.IsZero() {
		return nil
	}
	normalized := src.UTC()
	return &normalized
}

func cloneTime(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func normalizeKind(kind string) string {
	return strings.ToLower(strings.TrimSpace(kind))
}
