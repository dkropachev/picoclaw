package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"

	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
	"github.com/sipeed/picoclaw/pkg/routing"
)

// JSONLBackend adapts a memory.Store into the SessionStore interface.
// Write errors are logged rather than returned, matching the fire-and-forget
// contract of SessionManager that the agent loop relies on.
type JSONLBackend struct {
	store memory.Store
}

type metaAwareStore interface {
	GetSessionMeta(ctx context.Context, sessionKey string) (memory.SessionMeta, error)
	UpsertSessionMeta(ctx context.Context, sessionKey string, scope json.RawMessage, aliases []string) error
	ResolveSessionKey(ctx context.Context, sessionKey string) (string, bool, error)
}

type aliasPromotingStore interface {
	PromoteAliasHistory(ctx context.Context, sessionKey string, scope json.RawMessage, aliases []string) (bool, error)
}

type metaAdmittingStore interface {
	AdmitSessionMeta(
		ctx context.Context,
		sessionKey string,
		admit memory.SessionMetaAdmission,
	) (bool, error)
}

type snapshotStore interface {
	ReadSessionSnapshot(
		ctx context.Context,
		sessionKey string,
	) (canonicalKey string, history []providers.Message, meta memory.SessionMeta, found bool, err error)
}

type snapshotReplacingStore interface {
	ReplaceSessionSnapshot(ctx context.Context, replacement memory.SessionSnapshotReplacement) error
}

// MetadataAwareSessionStore exposes structured session metadata operations.
type MetadataAwareSessionStore interface {
	EnsureSessionMetadata(sessionKey string, scope *SessionScope, aliases []string)
	ResolveSessionKey(sessionKey string) string
	GetSessionScope(sessionKey string) *SessionScope
}

// NewJSONLBackend wraps a memory.Store for use as a SessionStore.
func NewJSONLBackend(store memory.Store) *JSONLBackend {
	return &JSONLBackend{store: store}
}

func (b *JSONLBackend) resolveSessionKey(sessionKey string) string {
	metaStore, ok := b.store.(metaAwareStore)
	if !ok {
		return sessionKey
	}
	resolved, found, err := metaStore.ResolveSessionKey(context.Background(), sessionKey)
	if err != nil {
		log.Printf("session: resolve session key: %v", err)
		return sessionKey
	}
	if found && resolved != "" {
		return resolved
	}
	return sessionKey
}

// ResolveSessionKey maps aliases onto their canonical session key when the
// underlying store supports structured metadata. Unknown aliases fall back to
// the original input so existing callers remain compatible.
func (b *JSONLBackend) ResolveSessionKey(sessionKey string) string {
	return b.resolveSessionKey(sessionKey)
}

// EnsureSessionMetadata persists scope and alias metadata for a session.
func (b *JSONLBackend) EnsureSessionMetadata(sessionKey string, scope *SessionScope, aliases []string) {
	metaStore, ok := b.store.(metaAwareStore)
	if !ok {
		return
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return
	}

	var rawScope json.RawMessage
	if scope != nil {
		data, err := json.Marshal(scope)
		if err != nil {
			log.Printf("session: encode session scope: %v", err)
			return
		}
		rawScope = data
	}
	ctx := context.Background()
	if err := metaStore.UpsertSessionMeta(ctx, sessionKey, rawScope, aliases); err != nil {
		log.Printf("session: upsert session metadata: %v", err)
		return
	}

	if promotingStore, ok := b.store.(aliasPromotingStore); ok {
		if _, err := promotingStore.PromoteAliasHistory(ctx, sessionKey, rawScope, aliases); err != nil {
			log.Printf("session: promote alias history: %v", err)
		}
	}
}

// AdmitSessionScope atomically arbitrates a normal live turn against a
// protected review projection. The policy is evaluated by the adapter while
// the lower JSONL store holds the same locks used by snapshot replacement.
func (b *JSONLBackend) AdmitSessionScope(
	ctx context.Context,
	admission SessionScopeAdmission,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if admission.Key == "" || admission.Key != strings.TrimSpace(admission.Key) ||
		admission.Scope == nil {
		return false, errors.New("session: scope admission is invalid")
	}
	if admission.Mode != ScopeAdmissionLive && admission.Mode != ScopeAdmissionReview {
		return false, errors.New("session: scope admission mode is invalid")
	}
	initialAliases := append([]string(nil), admission.InitialAliases...)
	if err := validateSessionScopeAdmissionAliases(admission.Key, initialAliases); err != nil {
		return false, err
	}
	desiredScope := CloneScope(admission.Scope)
	if admission.Mode == ScopeAdmissionLive && isReviewAdmissionScope(desiredScope) {
		return false, ErrScopeAdmissionConflict
	}
	if admission.Mode == ScopeAdmissionReview {
		if admission.PreserveExistingScope {
			return false, errors.New(
				"session: review scope admission cannot preserve an existing scope",
			)
		}
		if err := validateReviewScopeAdmission(admission.Key, *desiredScope); err != nil {
			return false, err
		}
	}
	rawScope, err := json.Marshal(desiredScope)
	if err != nil {
		return false, fmt.Errorf("session: encode scope admission: %w", err)
	}

	store, ok := b.store.(metaAdmittingStore)
	if !ok {
		return false, fmt.Errorf("%w: store %T", ErrScopeAdmissionUnsupported, b.store)
	}
	updated, err := store.AdmitSessionMeta(
		ctx,
		admission.Key,
		func(existing memory.SessionMeta, exists bool) (
			memory.SessionMetaAdmissionDecision,
			error,
		) {
			var currentScope *SessionScope
			if len(existing.Scope) > 0 {
				var decoded SessionScope
				if decodeErr := json.Unmarshal(existing.Scope, &decoded); decodeErr != nil {
					return memory.SessionMetaAdmissionDecision{}, fmt.Errorf(
						"session: decode admitted scope: %w",
						decodeErr,
					)
				}
				currentScope = &decoded
			}

			switch admission.Mode {
			case ScopeAdmissionLive:
				if isReviewAdmissionScope(currentScope) {
					return memory.SessionMetaAdmissionDecision{}, ErrScopeAdmissionConflict
				}
				aliases := initialAliases
				// Internal continuation/system paths often know only the final key and
				// existing scope. Nil means they have no complete replacement alias set;
				// preserve the locked owner's aliases instead of silently removing them.
				if aliases == nil && exists {
					aliases = existing.Aliases
				}
				committedScope := rawScope
				// A nil/inferred caller scope must not be read before acquiring the
				// admission lock: doing so could roll a concurrent ordinary migration
				// back. Preserve the exact locked bytes when an owner already exists.
				if admission.PreserveExistingScope && currentScope != nil {
					committedScope = existing.Scope
				}
				return memory.SessionMetaAdmissionDecision{
					Scope:                  append(json.RawMessage(nil), committedScope...),
					Aliases:                append([]string(nil), aliases...),
					Update:                 true,
					PreserveRequestedAlias: true,
					PromoteAliasHistory:    true,
				}, nil
			case ScopeAdmissionReview:
				if exists {
					if existing.Key != admission.Key || currentScope == nil ||
						!isReviewAdmissionScope(currentScope) ||
						!reflect.DeepEqual(currentScope, desiredScope) {
						return memory.SessionMetaAdmissionDecision{}, ErrScopeAdmissionConflict
					}
					return memory.SessionMetaAdmissionDecision{}, nil
				}
				return memory.SessionMetaAdmissionDecision{
					Scope:            append(json.RawMessage(nil), rawScope...),
					Aliases:          append([]string(nil), initialAliases...),
					Update:           true,
					ExclusiveAliases: true,
				}, nil
			default:
				return memory.SessionMetaAdmissionDecision{}, errors.New(
					"session: scope admission mode is invalid",
				)
			}
		},
	)
	if err != nil {
		return false, err
	}
	return updated, nil
}

func isReviewAdmissionScope(scope *SessionScope) bool {
	return scope != nil && strings.EqualFold(strings.TrimSpace(scope.Channel), "review")
}

func validateReviewScopeAdmission(key string, scope SessionScope) error {
	if scope.Version != ScopeVersionV1 {
		return errors.New("session: review scope admission version is invalid")
	}
	if !routing.IsCanonicalAgentID(scope.AgentID) {
		return errors.New("session: review scope admission owner is invalid")
	}
	if err := validateCanonicalReplacementScope(scope); err != nil {
		return fmt.Errorf("session: review scope admission is not canonical: %w", err)
	}
	if scope.Channel != "review" {
		return errors.New("session: review scope admission requires review channel")
	}
	if BuildSessionKey(scope) != key {
		return errors.New("session: review scope admission key does not match its scope")
	}
	return nil
}

func validateSessionScopeAdmissionAliases(key string, aliases []string) error {
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		if alias == "" || alias != strings.TrimSpace(alias) || alias == key {
			return fmt.Errorf("session: scope admission alias %q is invalid", alias)
		}
		if _, exists := seen[alias]; exists {
			return fmt.Errorf("session: scope admission alias %q is duplicated", alias)
		}
		seen[alias] = struct{}{}
	}
	return nil
}

// GetSessionScope reads structured scope metadata for a session key or alias.
func (b *JSONLBackend) GetSessionScope(sessionKey string) *SessionScope {
	metaStore, ok := b.store.(metaAwareStore)
	if !ok {
		return nil
	}
	meta, err := metaStore.GetSessionMeta(context.Background(), sessionKey)
	if err != nil {
		log.Printf("session: get session metadata: %v", err)
		return nil
	}
	if len(meta.Scope) == 0 {
		return nil
	}
	var scope SessionScope
	if err := json.Unmarshal(meta.Scope, &scope); err != nil {
		log.Printf("session: decode session scope: %v", err)
		return nil
	}
	return CloneScope(&scope)
}

func (b *JSONLBackend) GetSessionMeta(ctx context.Context, sessionKey string) (memory.SessionMeta, error) {
	metaStore, ok := b.store.(metaAwareStore)
	if !ok {
		return memory.SessionMeta{}, nil
	}
	return metaStore.GetSessionMeta(ctx, sessionKey)
}

func (b *JSONLBackend) AddMessage(sessionKey, role, content string) {
	if err := b.store.AddMessage(context.Background(), sessionKey, role, content); err != nil {
		log.Printf("session: add message: %v", err)
	}
}

func (b *JSONLBackend) AddFullMessage(sessionKey string, msg providers.Message) {
	if err := b.store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
		log.Printf("session: add full message: %v", err)
	}
}

func (b *JSONLBackend) GetHistory(key string) []providers.Message {
	msgs, err := b.store.GetHistory(context.Background(), key)
	if err != nil {
		log.Printf("session: get history: %v", err)
		return []providers.Message{}
	}
	return msgs
}

// ReadSessionSnapshot performs a strict, non-mutating lookup. The underlying
// JSONL store resolves aliases and reads history, summary, and metadata under
// the canonical session lock, so callers cannot observe a torn session state.
func (b *JSONLBackend) ReadSessionSnapshot(
	ctx context.Context,
	key string,
) (SessionSnapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return SessionSnapshot{}, false, err
	}
	if strings.TrimSpace(key) == "" {
		return SessionSnapshot{}, false, nil
	}

	store, ok := b.store.(snapshotStore)
	if !ok {
		return SessionSnapshot{}, false, fmt.Errorf(
			"session: store %T does not support coherent snapshots",
			b.store,
		)
	}
	canonicalKey, history, meta, found, err := store.ReadSessionSnapshot(ctx, key)
	if err != nil || !found {
		return SessionSnapshot{}, found, err
	}

	var scope *SessionScope
	if len(meta.Scope) > 0 {
		var decoded SessionScope
		if err := json.Unmarshal(meta.Scope, &decoded); err != nil {
			return SessionSnapshot{}, false, fmt.Errorf("session: decode session scope: %w", err)
		}
		scope = CloneScope(&decoded)
	}

	return SessionSnapshot{
		Key:      canonicalKey,
		History:  cloneSessionMessages(history),
		Summary:  meta.Summary,
		Scope:    scope,
		Aliases:  append([]string(nil), meta.Aliases...),
		Revision: meta.Revision,
	}, true, nil
}

// ReplaceSessionSnapshot atomically compare-and-swaps a complete session
// tuple. The optional lower-store capability is required: falling back to the
// legacy setters could publish new metadata with old history, or vice versa.
func (b *JSONLBackend) ReplaceSessionSnapshot(
	ctx context.Context,
	replacement SessionSnapshotReplacement,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionSnapshotReplacement(replacement); err != nil {
		return err
	}

	store, ok := b.store.(snapshotReplacingStore)
	if !ok {
		return fmt.Errorf("%w: store %T", ErrSnapshotUnsupported, b.store)
	}

	rawScope, err := json.Marshal(replacement.Scope)
	if err != nil {
		return fmt.Errorf("session: encode replacement scope: %w", err)
	}
	err = store.ReplaceSessionSnapshot(ctx, memory.SessionSnapshotReplacement{
		Key:              replacement.Key,
		History:          cloneSessionMessages(replacement.History),
		Summary:          replacement.Summary,
		Scope:            append(json.RawMessage(nil), rawScope...),
		Aliases:          append([]string(nil), replacement.Aliases...),
		ExpectedRevision: replacement.ExpectedRevision,
	})
	if errors.Is(err, memory.ErrSnapshotConflict) {
		return fmt.Errorf("%w: %v", ErrSnapshotConflict, err)
	}
	if err != nil {
		return fmt.Errorf("session: replace snapshot: %w", err)
	}
	return nil
}

func validateSessionSnapshotReplacement(replacement SessionSnapshotReplacement) error {
	if replacement.Key != strings.TrimSpace(replacement.Key) ||
		!isExactOpaqueSessionKey(replacement.Key) {
		return errors.New("session: replacement key must be an exact opaque session key")
	}
	if replacement.Scope == nil {
		return errors.New("session: replacement scope is required")
	}
	if replacement.Scope.Version != ScopeVersionV1 {
		return errors.New("session: replacement scope version is invalid")
	}
	if !routing.IsCanonicalAgentID(replacement.Scope.AgentID) {
		return errors.New("session: replacement scope owner is invalid")
	}
	if err := validateCanonicalReplacementScope(*replacement.Scope); err != nil {
		return err
	}
	if BuildSessionKey(*replacement.Scope) != replacement.Key {
		return errors.New("session: replacement key does not match its scope")
	}

	seenAliases := make(map[string]struct{}, len(replacement.Aliases))
	for _, alias := range replacement.Aliases {
		if alias == "" || alias != strings.TrimSpace(alias) || alias == replacement.Key {
			return fmt.Errorf("session: replacement alias %q is invalid", alias)
		}
		if _, exists := seenAliases[alias]; exists {
			return fmt.Errorf("session: replacement alias %q is duplicated", alias)
		}
		seenAliases[alias] = struct{}{}
	}
	for messageIndex, message := range replacement.History {
		if messageutil.IsTransientAssistantThoughtMessage(message) {
			return fmt.Errorf("session: replacement message %d is transient", messageIndex)
		}
		if message.PromptLayer != "" || message.PromptSlot != "" || message.PromptSource != "" {
			return fmt.Errorf("session: replacement message %d has runtime prompt metadata", messageIndex)
		}
		for blockIndex, block := range message.SystemParts {
			if block.PromptLayer != "" || block.PromptSlot != "" || block.PromptSource != "" {
				return fmt.Errorf(
					"session: replacement message %d system block %d has runtime prompt metadata",
					messageIndex,
					blockIndex,
				)
			}
		}
		for callIndex, call := range message.ToolCalls {
			if call.Name != "" || call.Arguments != nil || call.ThoughtSignature != "" {
				return fmt.Errorf(
					"session: replacement message %d tool call %d has non-persistable runtime fields",
					messageIndex,
					callIndex,
				)
			}
		}
	}
	return nil
}

func validateCanonicalReplacementScope(scope SessionScope) error {
	if scope.Channel == "" || scope.Channel != strings.ToLower(strings.TrimSpace(scope.Channel)) {
		return errors.New("session: replacement scope channel is not canonical")
	}
	if scope.Account != routing.NormalizeAccountID(scope.Account) {
		return errors.New("session: replacement scope account is not canonical")
	}
	if len(scope.Values) != len(scope.Dimensions) {
		return errors.New("session: replacement scope values do not exactly match dimensions")
	}
	seen := make(map[string]struct{}, len(scope.Dimensions))
	for _, dimension := range scope.Dimensions {
		if dimension == "" || dimension != strings.ToLower(strings.TrimSpace(dimension)) {
			return errors.New("session: replacement scope dimension is not canonical")
		}
		if _, exists := seen[dimension]; exists {
			return fmt.Errorf("session: replacement scope dimension %q is duplicated", dimension)
		}
		seen[dimension] = struct{}{}
		value, exists := scope.Values[dimension]
		if !exists || value == "" || value != strings.ToLower(strings.TrimSpace(value)) {
			return fmt.Errorf("session: replacement scope value %q is not canonical", dimension)
		}
	}
	return nil
}

func isExactOpaqueSessionKey(key string) bool {
	if len(key) != len(sessionKeyV1Prefix)+64 ||
		!strings.HasPrefix(key, sessionKeyV1Prefix) {
		return false
	}
	for _, character := range key[len(sessionKeyV1Prefix):] {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (b *JSONLBackend) GetSummary(key string) string {
	summary, err := b.store.GetSummary(context.Background(), key)
	if err != nil {
		log.Printf("session: get summary: %v", err)
		return ""
	}
	return summary
}

func (b *JSONLBackend) SetSummary(key, summary string) {
	if err := b.store.SetSummary(context.Background(), key, summary); err != nil {
		log.Printf("session: set summary: %v", err)
	}
}

func (b *JSONLBackend) SetHistory(key string, history []providers.Message) {
	if err := b.store.SetHistory(context.Background(), key, history); err != nil {
		log.Printf("session: set history: %v", err)
	}
}

func (b *JSONLBackend) TruncateHistory(key string, keepLast int) {
	if err := b.store.TruncateHistory(context.Background(), key, keepLast); err != nil {
		log.Printf("session: truncate history: %v", err)
	}
}

// Save persists session state. Since the JSONL store fsyncs every write
// immediately, the data is already durable. Save runs compaction to reclaim
// space from logically truncated messages (no-op when there are none).
func (b *JSONLBackend) Save(key string) error {
	return b.store.Compact(context.Background(), key)
}

// Close releases resources held by the underlying store.
func (b *JSONLBackend) Close() error {
	return b.store.Close()
}

// ListSessions returns all known session keys.
func (b *JSONLBackend) ListSessions() []string {
	return b.store.ListSessions()
}
