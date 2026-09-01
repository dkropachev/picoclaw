package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
)

const (
	maxLineSize          = 10 * 1024 * 1024
	deleteManifestPrefix = ".session-delete-v1-"
)

var (
	ErrSnapshotConflict       = errors.New("memory: session snapshot conflict")
	ErrPendingSessionDeletion = errors.New("memory: session deletion recovery is pending")
)

// SessionMeta is the compatibility projection of typed SQLite session,
// scope, alias, and thread-link rows. Legacy-only offset/slot fields remain
// zero so source-compatible callers do not need an on-disk format branch.
type SessionMeta struct {
	Key               string            `json:"key"`
	Summary           string            `json:"summary"`
	Skip              int               `json:"skip"`
	Count             int               `json:"count"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	Scope             json.RawMessage   `json:"scope,omitempty"`
	Aliases           []string          `json:"aliases,omitempty"`
	ThreadType        string            `json:"thread_type,omitempty"`
	ThreadTitle       string            `json:"thread_title,omitempty"`
	ThreadContext     map[string]string `json:"thread_context,omitempty"`
	ThreadSourceQuery string            `json:"thread_source_query,omitempty"`
	ThreadID          string            `json:"thread_id,omitempty"`
	ThreadAttachedAt  time.Time         `json:"thread_attached_at,omitempty"`
	HistorySlot       string            `json:"history_slot,omitempty"`
	Revision          string            `json:"-"`
}

type SessionSnapshotReplacement struct {
	Key              string
	History          []providers.Message
	Summary          string
	Scope            json.RawMessage
	Aliases          []string
	ExpectedRevision string
}

type SessionMetaAdmissionDecision struct {
	Scope                  json.RawMessage
	Aliases                []string
	Update                 bool
	ExclusiveAliases       bool
	PreserveRequestedAlias bool
	PromoteAliasHistory    bool
}

type SessionMetaAdmission func(
	existing SessionMeta,
	exists bool,
) (SessionMetaAdmissionDecision, error)

type SessionMetaMutationState struct {
	SessionExists  bool
	MetadataExists bool
}

type sessionDeleteManifest struct {
	Version int      `json:"version"`
	Keys    []string `json:"keys"`
}

func snapshotRevision(key string, history []providers.Message, meta SessionMeta) (string, error) {
	meta = cloneSessionMeta(meta)
	meta.Revision = ""
	payload := struct {
		Key     string              `json:"key"`
		Meta    SessionMeta         `json:"meta"`
		History []providers.Message `json:"history"`
	}{Key: key, Meta: meta, History: history}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("memory: encode session snapshot revision: %w", err)
	}
	digest := sha256.Sum256(data)
	return "ssr_v1_" + hex.EncodeToString(digest[:]), nil
}

func normalizeAliases(canonicalKey string, aliases []string) []string {
	if len(aliases) == 0 {
		return nil
	}
	canonicalKey = strings.TrimSpace(canonicalKey)
	seen := make(map[string]struct{}, len(aliases))
	normalized := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == canonicalKey {
			continue
		}
		if _, duplicate := seen[alias]; duplicate {
			continue
		}
		seen[alias] = struct{}{}
		normalized = append(normalized, alias)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func cloneSessionMeta(meta SessionMeta) SessionMeta {
	meta.Scope = append(json.RawMessage(nil), meta.Scope...)
	meta.Aliases = append([]string(nil), meta.Aliases...)
	if meta.ThreadContext != nil {
		cloned := make(map[string]string, len(meta.ThreadContext))
		for key, value := range meta.ThreadContext {
			cloned[key] = value
		}
		meta.ThreadContext = cloned
	}
	return meta
}

func canonicalSessionScopeJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("memory: session scope admission scope is required")
	}
	canonical, err := canonicalJSONBlob(raw)
	if err != nil {
		return nil, fmt.Errorf("memory: decode session scope admission: %w", err)
	}
	return canonical, nil
}

func persistedSessionMetaEqual(left, right SessionMeta) (bool, error) {
	left = cloneSessionMeta(left)
	right = cloneSessionMeta(right)
	left.Revision = ""
	right.Revision = ""
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false, fmt.Errorf("memory: encode current metadata comparison: %w", err)
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false, fmt.Errorf("memory: encode expected metadata comparison: %w", err)
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func validateSnapshotReplacement(replacement SessionSnapshotReplacement) error {
	if replacement.Key == "" || replacement.Key != strings.TrimSpace(replacement.Key) {
		return errors.New("memory: session snapshot key is invalid")
	}
	var scope map[string]any
	if len(replacement.Scope) == 0 || json.Unmarshal(replacement.Scope, &scope) != nil || scope == nil {
		return errors.New("memory: session snapshot scope is invalid")
	}
	for index, message := range replacement.History {
		if messageutil.IsTransientAssistantThoughtMessage(message) {
			return fmt.Errorf("memory: session snapshot message %d is transient", index)
		}
		if message.PromptLayer != "" || message.PromptSlot != "" || message.PromptSource != "" {
			return fmt.Errorf("memory: session snapshot message %d has runtime prompt metadata", index)
		}
		for blockIndex, block := range message.SystemParts {
			if block.PromptLayer != "" || block.PromptSlot != "" || block.PromptSource != "" {
				return fmt.Errorf(
					"memory: session snapshot message %d system block %d has runtime prompt metadata",
					index, blockIndex,
				)
			}
		}
		for callIndex, call := range message.ToolCalls {
			if call.Name != "" || call.Arguments != nil || call.ThoughtSignature != "" {
				return fmt.Errorf(
					"memory: session snapshot message %d tool call %d has non-persistable runtime fields",
					index, callIndex,
				)
			}
		}
		if err := validateLegacyMessage(message); err != nil {
			return fmt.Errorf("memory: session snapshot message %d: %w", index, err)
		}
	}
	return nil
}

func isMainSessionAlias(alias string) bool {
	if strings.HasPrefix(alias, "agent:") && strings.HasSuffix(alias, ":main") {
		if parts := strings.SplitN(alias, ":", 4); len(parts) == 3 {
			return true
		}
	}
	if strings.HasPrefix(alias, "sk_v1_") {
		for _, agentID := range []string{"main", "Main", "MAIN"} {
			digest := sha256.Sum256([]byte("agent:" + agentID + ":main"))
			if "sk_v1_"+hex.EncodeToString(digest[:]) == alias {
				return true
			}
		}
	}
	return false
}
