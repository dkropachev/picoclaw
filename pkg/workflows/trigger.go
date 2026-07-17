package workflows

import (
	"fmt"
	"regexp"
	"strings"
)

type ChannelMessageEvent struct {
	Channel          string
	Account          string
	ChatID           string
	ChatType         string
	TopicID          string
	SpaceID          string
	SpaceType        string
	SenderID         string
	SenderUsername   string
	SenderName       string
	MessageID        string
	ReplyToMessageID string
	Mentioned        bool
	Text             string
	Media            []string
	ReplyHandles     map[string]string
	Raw              map[string]string
}

type ChannelMessageMatch struct {
	Inputs      map[string]any
	Event       map[string]any
	Session     string
	Delivery    Delivery
	Passthrough bool
}

func MatchChannelMessage(workflow *Workflow, ref string, event ChannelMessageEvent) (*ChannelMessageMatch, bool, error) {
	if workflow == nil || workflow.On.ChannelMessage == nil {
		return nil, false, nil
	}
	trigger := workflow.On.ChannelMessage
	if !stringListMatches(trigger.Channels, event.Channel, true) {
		return nil, false, nil
	}
	if !stringListMatches(trigger.Chats, event.ChatID, false) {
		return nil, false, nil
	}
	if !stringListMatches(trigger.Senders, event.SenderID, false) {
		return nil, false, nil
	}
	if trigger.Mentioned != nil && *trigger.Mentioned != event.Mentioned {
		return nil, false, nil
	}
	if trigger.Command != "" && !messageHasCommand(event.Text, trigger.Command) {
		return nil, false, nil
	}
	if strings.TrimSpace(trigger.TextMatches) != "" {
		re, err := regexp.Compile(trigger.TextMatches)
		if err != nil {
			return nil, false, err
		}
		if !re.MatchString(event.Text) {
			return nil, false, nil
		}
	}
	return &ChannelMessageMatch{
		Inputs:      map[string]any{"text": event.Text},
		Event:       event.ToMap(),
		Session:     ConversationSessionKey(ref, trigger.Conversation, event),
		Delivery:    ConversationDelivery(trigger.Conversation, event),
		Passthrough: boolPtrValue(trigger.Passthrough, false),
	}, true, nil
}

func MatchCommandMessage(workflow *Workflow, ref string, event ChannelMessageEvent) (*ChannelMessageMatch, bool, error) {
	if workflow == nil || workflow.On.Command == nil {
		return nil, false, nil
	}
	trigger := workflow.On.Command
	if !stringListMatches(trigger.Channels, event.Channel, true) {
		return nil, false, nil
	}
	if !stringListMatches(trigger.Chats, event.ChatID, false) {
		return nil, false, nil
	}
	if !stringListMatches(trigger.Senders, event.SenderID, false) {
		return nil, false, nil
	}
	if !messageHasCommand(event.Text, trigger.Name) {
		return nil, false, nil
	}
	args := parseCommandArgs(event.Text, trigger.Name)
	inputs := map[string]any{
		"text":    event.Text,
		"command": trigger.Name,
		"args":    args.Raw,
		"argv":    append([]string(nil), args.Argv...),
	}
	for name, input := range trigger.Args {
		value, ok := args.Named[name]
		if !ok && len(args.Positional) > 0 {
			value = args.Positional[0]
			args.Positional = args.Positional[1:]
			ok = true
		}
		if ok {
			inputs[name] = value
			continue
		}
		if input.Default != nil {
			inputs[name] = input.Default
			continue
		}
		if input.Required {
			return nil, false, fmt.Errorf("required command arg %q is missing", name)
		}
	}
	return &ChannelMessageMatch{
		Inputs:      inputs,
		Event:       event.ToMap(),
		Session:     ConversationSessionKey(ref, trigger.Conversation, event),
		Delivery:    ConversationDelivery(trigger.Conversation, event),
		Passthrough: boolPtrValue(trigger.Passthrough, false),
	}, true, nil
}

func (e ChannelMessageEvent) ToMap() map[string]any {
	return map[string]any{
		"channel": e.Channel,
		"chat": map[string]any{
			"id":       e.ChatID,
			"type":     e.ChatType,
			"topic_id": e.TopicID,
			"space_id": e.SpaceID,
		},
		"sender": map[string]any{
			"id":       e.SenderID,
			"username": e.SenderUsername,
			"name":     e.SenderName,
		},
		"message": map[string]any{
			"id":                  e.MessageID,
			"text":                e.Text,
			"reply_to_message_id": e.ReplyToMessageID,
			"mentioned":           e.Mentioned,
			"media":               append([]string(nil), e.Media...),
		},
		"raw": cloneStringMapAny(e.Raw),
	}
}

func ConversationSessionKey(ref string, spec ConversationSpec, event ChannelMessageEvent) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "workflow"
	}
	switch strings.ToLower(strings.TrimSpace(spec.Session)) {
	case "global":
		return "workflow:" + ref + ":global"
	case "sender":
		sender := strings.TrimSpace(event.SenderID)
		if sender == "" {
			sender = "unknown"
		}
		return fmt.Sprintf("workflow:%s:sender:%s:%s", ref, event.Channel, sender)
	case "", "discussion":
		parts := []string{"workflow", ref, "discussion", event.Channel, event.ChatID}
		if strings.TrimSpace(event.TopicID) != "" {
			parts = append(parts, "topic", event.TopicID)
		}
		return strings.Join(parts, ":")
	default:
		return "workflow:" + ref + ":discussion:" + event.Channel + ":" + event.ChatID
	}
}

func ConversationDelivery(spec ConversationSpec, event ChannelMessageEvent) Delivery {
	switch strings.ToLower(strings.TrimSpace(spec.Delivery)) {
	case "none":
		return Delivery{}
	case "", "same_discussion":
		replyTo := strings.TrimSpace(event.ReplyToMessageID)
		if replyTo == "" {
			replyTo = strings.TrimSpace(event.MessageID)
		}
		return Delivery{
			Channel:          event.Channel,
			ChatID:           event.ChatID,
			TopicID:          event.TopicID,
			ThreadTS:         event.TopicID,
			MessageID:        event.MessageID,
			ReplyToMessageID: replyTo,
			ReplyHandles:     cloneStringMap(event.ReplyHandles),
		}
	default:
		return Delivery{}
	}
}

func stringListMatches(values []string, candidate string, fold bool) bool {
	if len(values) == 0 {
		return true
	}
	candidate = strings.TrimSpace(candidate)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if fold {
			if strings.EqualFold(value, candidate) {
				return true
			}
			continue
		}
		if value == candidate {
			return true
		}
	}
	return false
}

func messageHasCommand(text, command string) bool {
	text = strings.TrimSpace(text)
	command = strings.TrimSpace(command)
	if command == "" {
		return true
	}
	if !strings.HasPrefix(command, "/") {
		command = "/" + command
	}
	if text == command {
		return true
	}
	return strings.HasPrefix(text, command+" ")
}

type parsedCommandArgs struct {
	Raw        string
	Argv       []string
	Named      map[string]string
	Positional []string
}

func parseCommandArgs(text, command string) parsedCommandArgs {
	text = strings.TrimSpace(text)
	command = strings.TrimSpace(command)
	if command != "" && !strings.HasPrefix(command, "/") {
		command = "/" + command
	}
	raw := strings.TrimSpace(strings.TrimPrefix(text, command))
	argv := strings.Fields(raw)
	out := parsedCommandArgs{
		Raw:   raw,
		Argv:  append([]string(nil), argv...),
		Named: make(map[string]string),
	}
	for i := 0; i < len(argv); i++ {
		item := argv[i]
		if !strings.HasPrefix(item, "--") || len(item) <= 2 {
			out.Positional = append(out.Positional, item)
			continue
		}
		keyValue := strings.TrimPrefix(item, "--")
		key, value, ok := strings.Cut(keyValue, "=")
		if !ok {
			key = keyValue
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
				value = argv[i+1]
				i++
			} else {
				value = "true"
			}
		}
		key = strings.TrimSpace(key)
		if key != "" {
			out.Named[key] = value
		}
	}
	return out
}

func boolPtrValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func cloneStringMapAny(values map[string]string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
