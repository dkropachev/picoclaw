package session

import (
	"context"
	"errors"
	"reflect"

	"github.com/sipeed/picoclaw/pkg/providers"
)

var (
	// ErrSnapshotConflict reports that a session changed after the caller read
	// the revision supplied in SessionSnapshotReplacement.ExpectedRevision.
	ErrSnapshotConflict = errors.New("session snapshot conflict")

	// ErrSnapshotUnsupported reports that a SessionStore cannot atomically
	// replace a complete session snapshot. Callers must not emulate replacement
	// with the legacy fire-and-forget setters because that can expose torn state.
	ErrSnapshotUnsupported = errors.New("session snapshot replacement unsupported")

	// ErrScopeAdmissionConflict reports that a live turn and a protected review
	// projection attempted to own the same session key.
	ErrScopeAdmissionConflict = errors.New("session scope admission conflict")

	// ErrScopeAdmissionUnsupported reports that a snapshot-capable store cannot
	// arbitrate structured scope ownership atomically.
	ErrScopeAdmissionUnsupported = errors.New("session scope admission unsupported")
)

type ScopeAdmissionMode uint8

const (
	ScopeAdmissionLive ScopeAdmissionMode = iota + 1
	ScopeAdmissionReview
)

// SessionScopeAdmission atomically establishes one structured scope before a
// caller may read or mutate the session. Live admission may migrate ordinary
// legacy metadata but never enters review scope; review admission reserves an
// absent key and otherwise preserves the existing protected tuple for strict
// binding validation.
type SessionScopeAdmission struct {
	Key            string
	Scope          *SessionScope
	InitialAliases []string
	Mode           ScopeAdmissionMode
	// PreserveExistingScope makes Scope an absent-session fallback for live
	// admission. When the locked owner already has a non-review scope, the
	// exact stored scope is retained instead of being replaced by a value read
	// before admission.
	PreserveExistingScope bool
}

// ScopeAdmitter is the optional atomic ownership capability shared by live
// turns and protected review projection.
type ScopeAdmitter interface {
	AdmitSessionScope(ctx context.Context, admission SessionScopeAdmission) (bool, error)
}

// SessionSnapshot is an immutable-by-convention, point-in-time view of an
// existing session. Key is always the canonical session key, even when the
// lookup was made through an alias.
//
// Readers must return a deep copy: callers may freely mutate History, Scope,
// and Aliases without changing the live session.
type SessionSnapshot struct {
	Key     string
	History []providers.Message
	Summary string
	Scope   *SessionScope
	// Aliases contains the exact committed aliases for Key.
	Aliases []string
	// Revision is an opaque compare-and-swap token. It may be empty for
	// read-only backends that do not implement SnapshotReplacer.
	Revision string
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

// SessionSnapshotReplacement is one exact, compare-and-swap replacement of a
// canonical session's visible history, summary, scope, and aliases. Key must be
// the opaque key derived from Scope. An empty ExpectedRevision means that the
// canonical session must not exist.
type SessionSnapshotReplacement struct {
	Key              string
	History          []providers.Message
	Summary          string
	Scope            *SessionScope
	Aliases          []string
	ExpectedRevision string
}

// SnapshotReplacer is the optional atomic replacement capability. Stores that
// do not implement it fail closed with ErrSnapshotUnsupported; replacement
// must never be synthesized from SessionStore's individual write methods.
type SnapshotReplacer interface {
	ReplaceSessionSnapshot(ctx context.Context, replacement SessionSnapshotReplacement) error
}

// CloneMessages returns a graph-detached copy of session messages, including
// nested tool arguments and pointer-backed protocol fields.
func CloneMessages(messages []providers.Message) []providers.Message {
	return cloneSessionMessages(messages)
}

// SessionStore defines the persistence operations used by the agent loop.
// Both the in-memory SessionManager and SQLiteBackend satisfy this interface.
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
	if message.Media != nil {
		cloned.Media = make([]string, len(message.Media))
		copy(cloned.Media, message.Media)
	}
	if message.Attachments != nil {
		cloned.Attachments = make([]providers.Attachment, len(message.Attachments))
		copy(cloned.Attachments, message.Attachments)
	}
	if message.Parts != nil {
		cloned.Parts = make([]providers.PromptPart, len(message.Parts))
		copy(cloned.Parts, message.Parts)
	}
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
