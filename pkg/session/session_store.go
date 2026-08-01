package session

import (
	"context"
	"reflect"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// SessionSnapshot is an immutable-by-convention, point-in-time view of an
// existing session. Key is always the canonical session key, even when the
// lookup was made through an alias.
//
// Readers must return a deep copy: callers may freely mutate History and Scope
// without changing the live session.
type SessionSnapshot struct {
	Key     string
	History []providers.Message
	Summary string
	Scope   *SessionScope
}

// SnapshotReader is the optional, strict read API used when a caller needs an
// existing session without the create-on-write/fallback semantics of
// SessionStore's legacy methods.
//
// ReadSessionSnapshot returns found=false for a blank or unknown key. It must
// not create or otherwise mutate a session. Implementations that support
// aliases return the canonical key in SessionSnapshot.Key.
type SnapshotReader interface {
	ReadSessionSnapshot(ctx context.Context, key string) (snapshot SessionSnapshot, found bool, err error)
}

// CloneMessages returns a graph-detached copy of session messages, including
// nested tool arguments and pointer-backed protocol fields.
func CloneMessages(messages []providers.Message) []providers.Message {
	return cloneSessionMessages(messages)
}

// SessionStore defines the persistence operations used by the agent loop.
// Both SessionManager (legacy JSON backend) and JSONLBackend satisfy this
// interface, allowing the storage layer to be swapped without touching the
// agent loop code.
//
// Write methods (Add*, Set*, Truncate*) are fire-and-forget: they do not
// return errors. Implementations should log failures internally. This
// matches the original SessionManager contract that the agent loop relies on.
type SessionStore interface {
	// AddMessage appends a simple role/content message to the session.
	AddMessage(sessionKey, role, content string)
	// AddFullMessage appends a complete message including tool calls.
	AddFullMessage(sessionKey string, msg providers.Message)
	// GetHistory returns the full message history for the session.
	GetHistory(key string) []providers.Message
	// GetSummary returns the conversation summary, or "" if none.
	GetSummary(key string) string
	// SetSummary replaces the conversation summary.
	SetSummary(key, summary string)
	// SetHistory replaces the full message history.
	SetHistory(key string, history []providers.Message)
	// TruncateHistory keeps only the last keepLast messages.
	TruncateHistory(key string, keepLast int)
	// Save persists any pending state to durable storage.
	Save(key string) error
	// ListSessions returns all known session keys.
	ListSessions() []string
	// Close releases resources held by the store.
	Close() error
}

func cloneSessionMessages(messages []providers.Message) []providers.Message {
	if messages == nil {
		return nil
	}

	cloned := make([]providers.Message, len(messages))
	for i := range messages {
		cloned[i] = cloneSessionMessage(messages[i])
	}
	return cloned
}

func cloneSessionMessage(message providers.Message) providers.Message {
	cloned := message
	if message.CreatedAt != nil {
		createdAt := *message.CreatedAt
		cloned.CreatedAt = &createdAt
	}
	cloned.Media = append([]string(nil), message.Media...)
	cloned.Attachments = append([]providers.Attachment(nil), message.Attachments...)
	cloned.Parts = append([]providers.PromptPart(nil), message.Parts...)
	if message.SystemParts != nil {
		cloned.SystemParts = make([]providers.ContentBlock, len(message.SystemParts))
		for i, block := range message.SystemParts {
			cloned.SystemParts[i] = block
			if block.CacheControl != nil {
				cacheControl := *block.CacheControl
				cloned.SystemParts[i].CacheControl = &cacheControl
			}
		}
	}
	if message.ToolCalls != nil {
		cloned.ToolCalls = make([]providers.ToolCall, len(message.ToolCalls))
		for i, call := range message.ToolCalls {
			cloned.ToolCalls[i] = call
			if call.Function != nil {
				function := *call.Function
				cloned.ToolCalls[i].Function = &function
			}
			if call.Arguments != nil {
				cloned.ToolCalls[i].Arguments = cloneSessionMap(call.Arguments)
			}
			if call.ExtraContent != nil {
				extra := *call.ExtraContent
				if call.ExtraContent.Google != nil {
					google := *call.ExtraContent.Google
					extra.Google = &google
				}
				cloned.ToolCalls[i].ExtraContent = &extra
			}
		}
	}
	return cloned
}

func cloneSessionMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	return cloneSessionValue(reflect.ValueOf(source), make(map[sessionCloneVisit]reflect.Value)).Interface().(map[string]any)
}

type sessionCloneVisit struct {
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

// cloneSessionValue copies JSON-like tool arguments while retaining their Go
// types and graph shape, including shared/cyclic maps, slices, and pointers.
func cloneSessionValue(value reflect.Value, seen map[sessionCloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Zero(reflect.TypeOf((*any)(nil)).Elem())
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneSessionValue(value.Elem(), seen)
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := sessionCloneVisit{typeOf: value.Type(), pointer: value.Pointer()}
		if cloned, ok := seen[visit]; ok {
			return cloned
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		seen[visit] = result
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(iterator.Key(), cloneSessionValue(iterator.Value(), seen))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := sessionCloneVisit{typeOf: value.Type(), pointer: value.Pointer(), length: value.Len()}
		if value.Len() > 0 {
			if cloned, ok := seen[visit]; ok {
				return cloned
			}
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		if value.Len() > 0 {
			seen[visit] = result
		}
		for i := 0; i < value.Len(); i++ {
			result.Index(i).Set(cloneSessionValue(value.Index(i), seen))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			result.Index(i).Set(cloneSessionValue(value.Index(i), seen))
		}
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := sessionCloneVisit{typeOf: value.Type(), pointer: value.Pointer()}
		if cloned, ok := seen[visit]; ok {
			return cloned
		}
		result := reflect.New(value.Type().Elem())
		seen[visit] = result
		result.Elem().Set(cloneSessionValue(value.Elem(), seen))
		return result
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for i := 0; i < value.NumField(); i++ {
			if result.Field(i).CanSet() && value.Field(i).CanInterface() {
				result.Field(i).Set(cloneSessionValue(value.Field(i), seen))
			}
		}
		return result
	default:
		return value
	}
}
