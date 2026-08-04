package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// The provider-neutral message JSON deliberately omits runtime-only prompt
// metadata and normalized tool-call fields. A frozen read-only session must
// preserve those fields exactly across a workflow wait or process restart, so
// its private persistence contract carries a parallel internal projection.
type frozenSessionToolCallInternal struct {
	Name             string         `json:"name,omitempty"`
	Arguments        map[string]any `json:"arguments"`
	ThoughtSignature string         `json:"thought_signature,omitempty"`
}

type frozenSessionContentInternal struct {
	PromptLayer  string `json:"prompt_layer,omitempty"`
	PromptSlot   string `json:"prompt_slot,omitempty"`
	PromptSource string `json:"prompt_source,omitempty"`
}

type frozenSessionMessageInternal struct {
	PromptLayer  string                          `json:"prompt_layer,omitempty"`
	PromptSlot   string                          `json:"prompt_slot,omitempty"`
	PromptSource string                          `json:"prompt_source,omitempty"`
	SystemParts  []frozenSessionContentInternal  `json:"system_parts"`
	ToolCalls    []frozenSessionToolCallInternal `json:"tool_calls"`
}

type frozenSessionSnapshotJSON struct {
	Key      string                `json:"key"`
	History  []providers.Message   `json:"history"`
	Summary  string                `json:"summary,omitempty"`
	Scope    *session.SessionScope `json:"scope,omitempty"`
	Aliases  []string              `json:"aliases,omitempty"`
	Revision string                `json:"revision,omitempty"`
}

type frozenReadOnlySessionJSON struct {
	AgentID         string                         `json:"agent_id"`
	Snapshot        frozenSessionSnapshotJSON      `json:"snapshot"`
	HistoryRevision string                         `json:"history_revision"`
	FrozenMedia     media.FrozenSet                `json:"frozen_media"`
	Internal        []frozenSessionMessageInternal `json:"internal"`
}

func (frozen FrozenReadOnlySession) MarshalJSON() ([]byte, error) {
	if err := validateFrozenReadOnlySessionWithContext(
		context.Background(),
		&frozen,
		frozen.AgentID,
	); err != nil {
		return nil, err
	}
	internal := make([]frozenSessionMessageInternal, len(frozen.Snapshot.History))
	for messageIndex, message := range frozen.Snapshot.History {
		projected := frozenSessionMessageInternal{
			PromptLayer:  message.PromptLayer,
			PromptSlot:   message.PromptSlot,
			PromptSource: message.PromptSource,
			SystemParts:  make([]frozenSessionContentInternal, len(message.SystemParts)),
			ToolCalls:    make([]frozenSessionToolCallInternal, len(message.ToolCalls)),
		}
		for blockIndex, block := range message.SystemParts {
			projected.SystemParts[blockIndex] = frozenSessionContentInternal{
				PromptLayer:  block.PromptLayer,
				PromptSlot:   block.PromptSlot,
				PromptSource: block.PromptSource,
			}
		}
		for callIndex, call := range message.ToolCalls {
			projected.ToolCalls[callIndex] = frozenSessionToolCallInternal{
				Name:             call.Name,
				Arguments:        call.Arguments,
				ThoughtSignature: call.ThoughtSignature,
			}
		}
		internal[messageIndex] = projected
	}
	return json.Marshal(frozenReadOnlySessionJSON{
		AgentID: frozen.AgentID,
		Snapshot: frozenSessionSnapshotJSON{
			Key:      frozen.Snapshot.Key,
			History:  frozen.Snapshot.History,
			Summary:  frozen.Snapshot.Summary,
			Scope:    frozen.Snapshot.Scope,
			Aliases:  frozen.Snapshot.Aliases,
			Revision: frozen.Snapshot.Revision,
		},
		HistoryRevision: frozen.HistoryRevision,
		FrozenMedia:     frozen.FrozenMedia,
		Internal:        internal,
	})
}

func (frozen *FrozenReadOnlySession) UnmarshalJSON(data []byte) error {
	if frozen == nil {
		return fmt.Errorf("frozen read-only session target is nil")
	}
	if len(data) == 0 || len(data) > MaxWorkflowPrivateRootBytes ||
		!utf8.Valid(data) || !validPrivateSessionJSONStringEncoding(data) {
		return ErrPrivateWorkflowContext
	}
	if err := validateFrozenReadOnlySessionJSONEnvelope(data); err != nil {
		return err
	}
	if err := rejectDuplicatePrivateSessionJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var encoded frozenReadOnlySessionJSON
	if err := decoder.Decode(&encoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("frozen read-only session contains multiple values")
		}
		return err
	}
	if len(encoded.Internal) != len(encoded.Snapshot.History) {
		return fmt.Errorf("frozen read-only session internal history shape is invalid")
	}
	for messageIndex := range encoded.Snapshot.History {
		message := &encoded.Snapshot.History[messageIndex]
		internal := encoded.Internal[messageIndex]
		if len(internal.SystemParts) != len(message.SystemParts) ||
			len(internal.ToolCalls) != len(message.ToolCalls) {
			return fmt.Errorf("frozen read-only session internal message shape is invalid")
		}
		message.PromptLayer = internal.PromptLayer
		message.PromptSlot = internal.PromptSlot
		message.PromptSource = internal.PromptSource
		for blockIndex := range message.SystemParts {
			block := &message.SystemParts[blockIndex]
			block.PromptLayer = internal.SystemParts[blockIndex].PromptLayer
			block.PromptSlot = internal.SystemParts[blockIndex].PromptSlot
			block.PromptSource = internal.SystemParts[blockIndex].PromptSource
		}
		for callIndex := range message.ToolCalls {
			call := &message.ToolCalls[callIndex]
			call.Name = internal.ToolCalls[callIndex].Name
			call.Arguments = internal.ToolCalls[callIndex].Arguments
			call.ThoughtSignature = internal.ToolCalls[callIndex].ThoughtSignature
		}
	}
	candidate := FrozenReadOnlySession{
		AgentID: encoded.AgentID,
		Snapshot: session.SessionSnapshot{
			Key:      encoded.Snapshot.Key,
			History:  encoded.Snapshot.History,
			Summary:  encoded.Snapshot.Summary,
			Scope:    encoded.Snapshot.Scope,
			Aliases:  encoded.Snapshot.Aliases,
			Revision: encoded.Snapshot.Revision,
		},
		HistoryRevision: encoded.HistoryRevision,
		FrozenMedia:     encoded.FrozenMedia,
	}
	if err := validateFrozenReadOnlySessionWithContext(
		context.Background(),
		&candidate,
		candidate.AgentID,
	); err != nil {
		return err
	}
	*frozen = candidate
	return nil
}

func validateFrozenReadOnlySessionJSONEnvelope(data []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return ErrPrivateWorkflowContext
	}
	required := [...]string{
		"agent_id",
		"snapshot",
		"history_revision",
		"frozen_media",
		"internal",
	}
	if len(envelope) != len(required) {
		return ErrPrivateWorkflowContext
	}
	for _, name := range required {
		raw, ok := envelope[name]
		if !ok || len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrPrivateWorkflowContext
		}
	}
	return nil
}

func rejectDuplicatePrivateSessionJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeUniquePrivateSessionJSONValue(decoder, 0); err != nil {
		return ErrPrivateWorkflowContext
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrPrivateWorkflowContext
	}
	return nil
}

func consumeUniquePrivateSessionJSONValue(decoder *json.Decoder, depth int) error {
	const maxPrivateSessionJSONDepth = 128
	if depth > maxPrivateSessionJSONDepth {
		return ErrPrivateWorkflowContext
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrPrivateWorkflowContext
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return ErrPrivateWorkflowContext
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrPrivateWorkflowContext
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrPrivateWorkflowContext
			}
			seen[key] = struct{}{}
			if err := consumeUniquePrivateSessionJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return ErrPrivateWorkflowContext
		}
	case '[':
		for decoder.More() {
			if err := consumeUniquePrivateSessionJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return ErrPrivateWorkflowContext
		}
	default:
		return ErrPrivateWorkflowContext
	}
	return nil
}

func validPrivateSessionJSONStringEncoding(data []byte) bool {
	for index := 0; index < len(data); index++ {
		if data[index] != '"' {
			continue
		}
		index++
		for {
			if index >= len(data) {
				return false
			}
			switch data[index] {
			case '"':
				goto stringClosed
			case '\\':
				index++
				if index >= len(data) {
					return false
				}
				if data[index] != 'u' {
					if !strings.ContainsRune(`"\\/bfnrt`, rune(data[index])) {
						return false
					}
					index++
					continue
				}
				code, ok := privateSessionJSONHexQuad(data, index+1)
				if !ok {
					return false
				}
				index += 5
				switch {
				case code >= 0xd800 && code <= 0xdbff:
					if index+6 > len(data) || data[index] != '\\' || data[index+1] != 'u' {
						return false
					}
					low, lowOK := privateSessionJSONHexQuad(data, index+2)
					if !lowOK || low < 0xdc00 || low > 0xdfff {
						return false
					}
					index += 6
				case code >= 0xdc00 && code <= 0xdfff:
					return false
				}
				continue
			default:
				if data[index] < 0x20 {
					return false
				}
				_, size := utf8.DecodeRune(data[index:])
				index += size
			}
		}
	stringClosed:
	}
	return true
}

func privateSessionJSONHexQuad(data []byte, offset int) (uint16, bool) {
	if offset < 0 || offset+4 > len(data) {
		return 0, false
	}
	var value uint16
	for _, character := range data[offset : offset+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
