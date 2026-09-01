package session

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
)

// sanitizeFilename is retained for legacy fixture/import compatibility.
func sanitizeFilename(key string) string {
	return strings.NewReplacer(":", "_", "/", "_", "\\", "_").Replace(
		key,
	)
}

type Session struct {
	Key      string              `json:"key"`
	Messages []providers.Message `json:"messages"`
	Summary  string              `json:"summary,omitempty"`
	Created  time.Time           `json:"created"`
	Updated  time.Time           `json:"updated"`
}

// SessionManager is an in-memory store when constructed with an empty path.
// A nonempty legacy storage argument is a deprecated compatibility facade over
// SQLite and never writes aggregate JSON.
type SessionManager struct {
	sessions   map[string]*Session
	mu         sync.RWMutex
	persistent SessionStore
}

func NewSessionManager(storage string) *SessionManager {
	manager := &SessionManager{sessions: make(map[string]*Session)}
	if strings.TrimSpace(storage) == "" {
		return manager
	}
	backend, err := NewSQLiteBackend(storage)
	if err != nil {
		panic("open SQLite-backed SessionManager")
	}
	manager.persistent = backend
	return manager
}

func (sm *SessionManager) GetOrCreate(key string) *Session {
	if sm.persistent != nil {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if existing, ok := sm.sessions[key]; ok {
			return existing
		}
		now := time.Now()
		created := &Session{
			Key: key, Messages: sm.persistent.GetHistory(key),
			Summary: sm.persistent.GetSummary(key), Created: now, Updated: now,
		}
		sm.sessions[key] = created
		return created
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if existing, ok := sm.sessions[key]; ok {
		return existing
	}
	now := time.Now()
	created := &Session{Key: key, Messages: []providers.Message{}, Created: now, Updated: now}
	sm.sessions[key] = created
	return created
}

func ensureMessageCreatedAt(msg *providers.Message, fallback time.Time) {
	if msg.CreatedAt != nil && !msg.CreatedAt.IsZero() {
		return
	}
	ts := fallback
	msg.CreatedAt = &ts
}

func normalizeHistoryCreatedAt(history []providers.Message) {
	now := time.Now()
	for i := range history {
		ensureMessageCreatedAt(&history[i], now)
	}
}

func (sm *SessionManager) AddMessage(key, role, content string) {
	sm.AddFullMessage(key, providers.Message{Role: role, Content: content})
}

func (sm *SessionManager) AddFullMessage(key string, message providers.Message) {
	if sm.persistent != nil {
		sm.persistent.AddFullMessage(key, message)
		sm.mu.Lock()
		if stored, ok := sm.sessions[key]; ok && !messageutil.IsTransientAssistantThoughtMessage(message) {
			now := time.Now()
			ensureMessageCreatedAt(&message, now)
			stored.Messages = append(stored.Messages, cloneSessionMessage(message))
			stored.Updated = now
		}
		sm.mu.Unlock()
		return
	}
	if messageutil.IsTransientAssistantThoughtMessage(message) {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	now := time.Now()
	ensureMessageCreatedAt(&message, now)
	stored, ok := sm.sessions[key]
	if !ok {
		stored = &Session{Key: key, Messages: []providers.Message{}, Created: now}
		sm.sessions[key] = stored
	}
	stored.Messages = append(stored.Messages, cloneSessionMessage(message))
	stored.Updated = now
}

func (sm *SessionManager) GetHistory(key string) []providers.Message {
	if sm.persistent != nil {
		sm.mu.RLock()
		if stored, ok := sm.sessions[key]; ok {
			history := cloneSessionMessages(stored.Messages)
			sm.mu.RUnlock()
			return history
		}
		sm.mu.RUnlock()
		return sm.persistent.GetHistory(key)
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	stored, ok := sm.sessions[key]
	if !ok {
		return []providers.Message{}
	}
	return cloneSessionMessages(stored.Messages)
}

func (sm *SessionManager) ReadSessionSnapshot(
	ctx context.Context,
	key string,
) (SessionSnapshot, bool, error) {
	if sm.persistent != nil {
		if err := ctx.Err(); err != nil {
			return SessionSnapshot{}, false, err
		}
		sm.mu.RLock()
		if stored, ok := sm.sessions[key]; ok {
			snapshot := SessionSnapshot{
				Key: stored.Key, History: cloneSessionMessages(stored.Messages), Summary: stored.Summary,
			}
			sm.mu.RUnlock()
			return snapshot, true, nil
		}
		sm.mu.RUnlock()
	}
	if reader, ok := sm.persistent.(SnapshotReader); ok {
		return reader.ReadSessionSnapshot(ctx, key)
	}
	if err := ctx.Err(); err != nil {
		return SessionSnapshot{}, false, err
	}
	if strings.TrimSpace(key) == "" {
		return SessionSnapshot{}, false, nil
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	stored, ok := sm.sessions[key]
	if !ok {
		return SessionSnapshot{}, false, nil
	}
	return SessionSnapshot{
		Key: stored.Key, History: cloneSessionMessages(stored.Messages), Summary: stored.Summary,
	}, true, nil
}

func (sm *SessionManager) GetSummary(key string) string {
	if sm.persistent != nil {
		sm.mu.RLock()
		if stored, ok := sm.sessions[key]; ok {
			summary := stored.Summary
			sm.mu.RUnlock()
			return summary
		}
		sm.mu.RUnlock()
		return sm.persistent.GetSummary(key)
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if stored, ok := sm.sessions[key]; ok {
		return stored.Summary
	}
	return ""
}

func (sm *SessionManager) SetSummary(key, summary string) {
	if sm.persistent != nil {
		sm.persistent.SetSummary(key, summary)
		sm.mu.Lock()
		if stored, ok := sm.sessions[key]; ok {
			stored.Summary = summary
			stored.Updated = time.Now()
		}
		sm.mu.Unlock()
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if stored, ok := sm.sessions[key]; ok {
		stored.Summary = summary
		stored.Updated = time.Now()
	}
}

func (sm *SessionManager) SetHistory(key string, history []providers.Message) {
	if sm.persistent != nil {
		sm.persistent.SetHistory(key, history)
		sm.mu.Lock()
		if stored, ok := sm.sessions[key]; ok {
			stored.Messages = cloneSessionMessages(messageutil.FilterInvalidHistoryMessages(history))
			normalizeHistoryCreatedAt(stored.Messages)
			stored.Updated = time.Now()
		}
		sm.mu.Unlock()
		return
	}
	history = messageutil.FilterInvalidHistoryMessages(history)
	history = cloneSessionMessages(history)
	normalizeHistoryCreatedAt(history)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if stored, ok := sm.sessions[key]; ok {
		stored.Messages = history
		stored.Updated = time.Now()
	}
}

func (sm *SessionManager) TruncateHistory(key string, keepLast int) {
	if sm.persistent != nil {
		sm.persistent.TruncateHistory(key, keepLast)
		sm.mu.Lock()
		if stored, ok := sm.sessions[key]; ok {
			switch {
			case keepLast <= 0:
				stored.Messages = []providers.Message{}
			case len(stored.Messages) > keepLast:
				stored.Messages = cloneSessionMessages(stored.Messages[len(stored.Messages)-keepLast:])
			}
			stored.Updated = time.Now()
		}
		sm.mu.Unlock()
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	stored, ok := sm.sessions[key]
	if !ok {
		return
	}
	if keepLast <= 0 {
		stored.Messages = []providers.Message{}
	} else if len(stored.Messages) > keepLast {
		stored.Messages = cloneSessionMessages(stored.Messages[len(stored.Messages)-keepLast:])
	} else {
		return
	}
	stored.Updated = time.Now()
}

func (sm *SessionManager) Save(key string) error {
	if sm.persistent != nil {
		sm.mu.RLock()
		stored, ok := sm.sessions[key]
		var history []providers.Message
		var summary string
		if ok {
			history = cloneSessionMessages(stored.Messages)
			summary = stored.Summary
		}
		sm.mu.RUnlock()
		if ok {
			sm.persistent.SetHistory(key, history)
			sm.persistent.SetSummary(key, summary)
		}
		return sm.persistent.Save(key)
	}
	return nil
}

func (sm *SessionManager) ListSessions() []string {
	if sm.persistent != nil {
		keys := sm.persistent.ListSessions()
		seen := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			seen[key] = struct{}{}
		}
		sm.mu.RLock()
		for key := range sm.sessions {
			if _, ok := seen[key]; !ok {
				keys = append(keys, key)
			}
		}
		sm.mu.RUnlock()
		return keys
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	keys := make([]string, 0, len(sm.sessions))
	for key := range sm.sessions {
		keys = append(keys, key)
	}
	return keys
}

func (sm *SessionManager) Close() error {
	if sm.persistent != nil {
		return sm.persistent.Close()
	}
	return nil
}
