package memory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
)

const (
	// numLockShards is the fixed number of shared mutexes used to serialize
	// per-directory and per-session access. Using sharded arrays instead of maps keeps
	// memory bounded regardless of how many sessions are created over
	// the lifetime of the process — important for a long-running daemon.
	numLockShards = 64

	// maxLineSize is the maximum size of a single JSON line in a .jsonl
	// file. Tool results (read_file, web search, etc.) can be large, so
	// we set a generous limit. The scanner starts at 64 KB and grows
	// only as needed up to this cap.
	maxLineSize = 10 * 1024 * 1024 // 10 MB

	deleteManifestPrefix = ".session-delete-v1-"
	deleteManifestSuffix = ".json"
)

var (
	// ErrSnapshotConflict reports that the committed session tuple changed
	// since the caller captured ExpectedRevision.
	ErrSnapshotConflict = errors.New("memory: session snapshot conflict")
	// ErrPendingSessionDeletion reports that a durable grouped-deletion intent
	// still needs recovery. Existing stores fail closed instead of creating a
	// new generation that a later recovery would erase.
	ErrPendingSessionDeletion = errors.New("memory: session deletion recovery is pending")

	// Store instances rooted at the same directory must coordinate. A fixed
	// shard set keeps memory bounded while covering callers that independently
	// construct JSONLStore values (the web and thread stores do this today).
	sharedSessionLocks   [numLockShards]sync.Mutex
	sharedDirectoryLocks [numLockShards]sync.RWMutex
	pendingDeletions     = struct {
		sync.RWMutex
		directories map[string]struct{}
	}{directories: make(map[string]struct{})}
)

// SessionMeta holds per-session metadata stored in a .meta.json file.
//
// Scope is stored as raw JSON so pkg/memory can stay decoupled from the
// higher-level session package while still preserving structured scope data.
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

// SessionSnapshotReplacement is the lower storage-layer representation of an
// exact session tuple replacement. Empty ExpectedRevision means the canonical
// session must not have a committed base history or metadata file.
type SessionSnapshotReplacement struct {
	Key              string
	History          []providers.Message
	Summary          string
	Scope            json.RawMessage
	Aliases          []string
	ExpectedRevision string
}

type jsonlStoreHooks struct {
	writeHistory        func(string, []byte, os.FileMode) error
	writeMeta           func(string, []byte, os.FileMode) error
	writeDeleteManifest func(string, []byte, os.FileMode) error
	removeFile          func(string) error
	afterResolve        func(requestedKey, canonicalKey string)
}

type sessionDeleteManifest struct {
	Version int      `json:"version"`
	Keys    []string `json:"keys"`
}

// JSONLStore implements Store using append-only JSONL files.
//
// Legacy sessions use one history file plus metadata:
//
//	{sanitized_key}.jsonl      — one JSON-encoded message per line, append-only
//	{sanitized_key}.meta.json  — session metadata (summary, logical truncation offset)
//
// Whole-history rewrites upgrade to two bounded files, .history-a and
// .history-b. Metadata selects exactly one active file; writers durably replace
// the inactive file before atomically flipping that selector. Ordinary appends
// continue on the selected file. TruncateHistory remains logical until Compact
// rotates the visible messages into the inactive slot.
type JSONLStore struct {
	dir   string
	hooks jsonlStoreHooks
}

// NewJSONLStore creates a new JSONL-backed store rooted at dir.
func NewJSONLStore(dir string) (*JSONLStore, error) {
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return nil, fmt.Errorf("memory: create directory: %w", err)
	}
	cleanDir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("memory: resolve directory: %w", err)
	}
	store := &JSONLStore{
		dir: cleanDir,
		hooks: jsonlStoreHooks{
			writeHistory:        fileutil.WriteFileAtomic,
			writeMeta:           fileutil.WriteFileAtomic,
			writeDeleteManifest: fileutil.WriteFileAtomic,
			removeFile:          fileutil.RemoveDurable,
		},
	}
	directoryLock := store.directoryLock()
	directoryLock.Lock()
	defer directoryLock.Unlock()
	if err := store.recoverPendingDeletionsLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

// sessionLock returns a mutex for the given session key.
// Keys are mapped to a fixed pool of shards via FNV hash, so
// memory usage is O(1) regardless of total session count.
func (s *JSONLStore) sessionLock(key string) *sync.Mutex {
	return &sharedSessionLocks[s.sessionLockShard(key)]
}

func (s *JSONLStore) sessionLockShard(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s.dir))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return h.Sum32() % numLockShards
}

func (s *JSONLStore) directoryLock() *sync.RWMutex {
	h := fnv.New32a()
	h.Write([]byte(s.dir))
	return &sharedDirectoryLocks[h.Sum32()%numLockShards]
}

func (s *JSONLStore) markPendingDeletion() {
	pendingDeletions.Lock()
	pendingDeletions.directories[s.dir] = struct{}{}
	pendingDeletions.Unlock()
}

func (s *JSONLStore) clearPendingDeletion() {
	pendingDeletions.Lock()
	delete(pendingDeletions.directories, s.dir)
	pendingDeletions.Unlock()
}

func (s *JSONLStore) pendingDeletionError() error {
	pendingDeletions.RLock()
	_, pending := pendingDeletions.directories[s.dir]
	pendingDeletions.RUnlock()
	if pending {
		return ErrPendingSessionDeletion
	}
	return nil
}

func (s *JSONLStore) jsonlPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".jsonl")
}

func (s *JSONLStore) metaPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".meta.json")
}

func (s *JSONLStore) historySlotPath(key, slot string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".history-"+slot)
}

func (s *JSONLStore) historyPath(key string, meta SessionMeta) (string, error) {
	switch meta.HistorySlot {
	case "":
		return s.jsonlPath(key), nil
	case "a", "b":
		return s.historySlotPath(key, meta.HistorySlot), nil
	default:
		return "", fmt.Errorf("memory: invalid history slot %q", meta.HistorySlot)
	}
}

func inactiveHistorySlot(active string) (string, error) {
	switch active {
	case "", "b":
		return "a", nil
	case "a":
		return "b", nil
	default:
		return "", fmt.Errorf("memory: invalid history slot %q", active)
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (s *JSONLStore) committedHistoryPath(key string, meta SessionMeta) (string, error) {
	path, err := s.historyPath(key, meta)
	if err != nil {
		return "", err
	}
	if meta.HistorySlot == "" {
		return path, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("memory: active history slot %q is missing", meta.HistorySlot)
		}
		return "", fmt.Errorf("memory: stat active history slot: %w", err)
	}
	return path, nil
}

func snapshotRevision(key string, history []providers.Message, meta SessionMeta) (string, error) {
	meta = cloneSessionMeta(meta)
	meta.Revision = ""
	payload := struct {
		Key     string              `json:"key"`
		Meta    SessionMeta         `json:"meta"`
		History []providers.Message `json:"history"`
	}{
		Key:     key,
		Meta:    meta,
		History: history,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("memory: encode session snapshot revision: %w", err)
	}
	digest := sha256.Sum256(data)
	return "ssr_v1_" + hex.EncodeToString(digest[:]), nil
}

// sanitizeKey converts a session key to a safe filename component.
// Mirrors pkg/session.sanitizeFilename so that migration paths match.
// Replaces ':' with '_' (session key separator) and '/' and '\' with '_'
// so composite IDs (e.g. Telegram forum "chatID/threadID", Slack "channel/thread_ts")
// do not create subdirectories or break on Windows.
func sanitizeKey(key string) string {
	s := strings.ReplaceAll(key, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// readMeta loads the metadata file for a session.
// Returns a zero-value sessionMeta if the file does not exist.
func (s *JSONLStore) readMeta(key string) (SessionMeta, error) {
	data, err := os.ReadFile(s.metaPath(key))
	if os.IsNotExist(err) {
		return SessionMeta{Key: key}, nil
	}
	if err != nil {
		return SessionMeta{}, fmt.Errorf("memory: read meta: %w", err)
	}
	var meta SessionMeta
	err = json.Unmarshal(data, &meta)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("memory: decode meta: %w", err)
	}
	if meta.Key == "" {
		meta.Key = key
	}
	return meta, nil
}

func (s *JSONLStore) readMetaStrict(key string) (SessionMeta, error) {
	data, err := os.ReadFile(s.metaPath(key))
	if os.IsNotExist(err) {
		return SessionMeta{}, fmt.Errorf("memory: session metadata is missing")
	}
	if err != nil {
		return SessionMeta{}, fmt.Errorf("memory: read meta: %w", err)
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SessionMeta{}, fmt.Errorf("memory: decode meta: %w", err)
	}
	if strings.TrimSpace(meta.Key) == "" {
		return SessionMeta{}, fmt.Errorf("memory: session metadata key is missing")
	}
	if err := validateMetaOffsets(meta); err != nil {
		return SessionMeta{}, err
	}
	return meta, nil
}

func validateMetaOffsets(meta SessionMeta) error {
	if meta.Skip < 0 {
		return fmt.Errorf("memory: session metadata skip is negative: %d", meta.Skip)
	}
	if meta.Count < 0 {
		return fmt.Errorf("memory: session metadata count is negative: %d", meta.Count)
	}
	if meta.Skip > meta.Count {
		return fmt.Errorf(
			"memory: session metadata skip %d exceeds count %d",
			meta.Skip,
			meta.Count,
		)
	}
	return nil
}

// writeMeta atomically writes the metadata file using the project's
// standard WriteFileAtomic (temp + fsync + rename).
func (s *JSONLStore) writeMeta(key string, meta SessionMeta) error {
	if strings.TrimSpace(meta.Key) == "" {
		meta.Key = key
	}
	if meta.Key != key {
		return fmt.Errorf("memory: session metadata key %q does not match canonical key %q", meta.Key, key)
	}
	if _, err := s.historyPath(key, meta); err != nil {
		return err
	}
	if validationErr := validateMetaOffsets(meta); validationErr != nil {
		return validationErr
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encode meta: %w", err)
	}
	return s.hooks.writeMeta(s.metaPath(key), data, 0o644)
}

func cloneRawJSON(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), data...)
}

func normalizeAliases(canonicalKey string, aliases []string) []string {
	if len(aliases) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	canonicalKey = strings.TrimSpace(canonicalKey)
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == canonicalKey {
			continue
		}
		if _, ok := seen[alias]; ok {
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

func (s *JSONLStore) validateAliasesAvailableLocked(
	canonicalKey string,
	aliases []string,
	existingAliases []string,
) (map[string]struct{}, map[string]struct{}, error) {
	preservedDirectShadows := make(map[string]struct{})
	for _, alias := range aliases {
		exists, err := s.sessionExistsStrict(alias)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			if !slices.Contains(existingAliases, alias) {
				return nil, nil, fmt.Errorf("memory: session alias %q already has session data", alias)
			}
			// PromoteAliasHistory intentionally retains legacy files after copying
			// them. An already-owned direct shadow may be preserved, but a new
			// binding may never claim direct session data.
			preservedDirectShadows[alias] = struct{}{}
		}
	}
	preservedAmbiguous, err := s.validateAliasOwnershipLocked(
		canonicalKey,
		aliases,
		existingAliases,
		false,
	)
	if err != nil {
		return nil, nil, err
	}
	return preservedDirectShadows, preservedAmbiguous, nil
}

func (s *JSONLStore) validateAliasOwnershipLocked(
	canonicalKey string,
	aliases []string,
	existingAliases []string,
	allowSharedLegacyAliases bool,
) (map[string]struct{}, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("memory: read sessions dir: %w", err)
	}
	wanted := make(map[string]struct{}, len(aliases))
	preservedAmbiguous := make(map[string]struct{})
	for _, alias := range aliases {
		if allowSharedLegacyAliases && isMainSessionAlias(alias) {
			continue
		}
		wanted[alias] = struct{}{}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("memory: read session metadata %s: %w", entry.Name(), err)
		}
		var candidate SessionMeta
		if err := json.Unmarshal(data, &candidate); err != nil {
			return nil, fmt.Errorf("memory: decode session metadata %s: %w", entry.Name(), err)
		}
		candidate.Key = strings.TrimSpace(candidate.Key)
		if candidate.Key == "" {
			return nil, fmt.Errorf("memory: decode session metadata %s: missing key", entry.Name())
		}
		if candidate.Key == canonicalKey {
			continue
		}
		if _, collision := wanted[candidate.Key]; collision &&
			shouldShortCircuitSessionResolve(candidate.Key) {
			if slices.Contains(existingAliases, candidate.Key) {
				preservedAmbiguous[candidate.Key] = struct{}{}
			} else {
				return nil, fmt.Errorf(
					"memory: session alias %q is already a canonical session key",
					candidate.Key,
				)
			}
		}
		for _, alias := range candidate.Aliases {
			if allowSharedLegacyAliases && isMainSessionAlias(alias) {
				continue
			}
			if alias == canonicalKey {
				return nil, fmt.Errorf("memory: session key %q is already an alias", canonicalKey)
			}
			if _, collision := wanted[alias]; collision {
				if slices.Contains(existingAliases, alias) {
					// Legacy allocation intentionally assigned some fallback aliases
					// to multiple sessions. Exact replacement may preserve such an
					// alias, but may never introduce the ambiguity.
					preservedAmbiguous[alias] = struct{}{}
					continue
				}
				if allowSharedLegacyAliases && strings.HasPrefix(alias, "agent:") {
					// Allocators intentionally emit broad legacy fallbacks that can
					// be shared by scopes split on account or other dimensions.
					// Metadata initialization may retain that compatibility;
					// strict whole-snapshot replacement may not create it.
					preservedAmbiguous[alias] = struct{}{}
					continue
				}
				return nil, fmt.Errorf(
					"memory: session alias %q maps to multiple canonical keys",
					alias,
				)
			}
		}
	}
	return preservedAmbiguous, nil
}

func (s *JSONLStore) sessionExists(key string) bool {
	if key == "" {
		return false
	}
	if _, err := os.Stat(s.jsonlPath(key)); err == nil {
		return true
	}
	if _, err := os.Stat(s.metaPath(key)); err == nil {
		return true
	}
	return false
}

func (s *JSONLStore) sessionExistsStrict(key string) (bool, error) {
	if strings.TrimSpace(key) == "" {
		return false, nil
	}
	for _, path := range []string{s.jsonlPath(key), s.metaPath(key)} {
		_, err := os.Stat(path)
		switch {
		case err == nil:
			return true, nil
		case os.IsNotExist(err):
			continue
		default:
			return false, fmt.Errorf("memory: stat session data: %w", err)
		}
	}
	return false, nil
}

func cloneSessionMeta(meta SessionMeta) SessionMeta {
	meta.Scope = cloneRawJSON(meta.Scope)
	if meta.Aliases != nil {
		meta.Aliases = append([]string(nil), meta.Aliases...)
	}
	if meta.ThreadContext != nil {
		threadContext := make(map[string]string, len(meta.ThreadContext))
		for key, value := range meta.ThreadContext {
			threadContext[key] = value
		}
		meta.ThreadContext = threadContext
	}
	return meta
}

// GetSessionMeta returns the current metadata snapshot for sessionKey.
func (s *JSONLStore) GetSessionMeta(ctx context.Context, sessionKey string) (SessionMeta, error) {
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return SessionMeta{}, err
	}
	defer unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return SessionMeta{}, err
	}
	return cloneSessionMeta(meta), nil
}

// ReadSessionState returns one tolerant, coherent session tuple for browser
// and thread projections. Malformed JSONL records keep the legacy recovery
// behavior; an invalid selector or missing selected slot fails closed.
func (s *JSONLStore) ReadSessionState(
	ctx context.Context,
	sessionKey string,
) ([]providers.Message, SessionMeta, time.Time, error) {
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	defer unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	path, err := s.committedHistoryPath(sessionKey, meta)
	if err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	history, err := readMessages(path, meta.Skip)
	if err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	var modifiedAt time.Time
	if info, statErr := os.Stat(path); statErr == nil {
		modifiedAt = info.ModTime()
	} else if !os.IsNotExist(statErr) {
		return nil, SessionMeta{}, time.Time{}, fmt.Errorf("memory: stat history: %w", statErr)
	}
	if err := contextError(ctx); err != nil {
		return nil, SessionMeta{}, time.Time{}, err
	}
	return history, cloneSessionMeta(meta), modifiedAt, nil
}

// UpsertSessionMeta stores structured session metadata while preserving
// summary/count/skip timestamps maintained by the core JSONL store.
func (s *JSONLStore) UpsertSessionMeta(
	ctx context.Context,
	sessionKey string,
	scope json.RawMessage,
	aliases []string,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	directoryLock := s.directoryLock()
	directoryLock.Lock()
	defer directoryLock.Unlock()
	if pendingErr := s.pendingDeletionError(); pendingErr != nil {
		return pendingErr
	}

	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	existingAliases := append([]string(nil), meta.Aliases...)
	normalizedAliases := normalizeAliases(sessionKey, aliases)
	if _, err := s.validateAliasOwnershipLocked(
		sessionKey,
		normalizedAliases,
		existingAliases,
		true,
	); err != nil {
		return err
	}
	meta.Scope = cloneRawJSON(scope)
	meta.Aliases = normalizedAliases
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

// UpdateSessionMeta applies a coordinated metadata mutation. It is intended
// for adjacent stores, such as threads, that own fields in SessionMeta but
// must not stale-clobber history-slot commits.
func (s *JSONLStore) UpdateSessionMeta(
	ctx context.Context,
	sessionKey string,
	update func(*SessionMeta) error,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || update == nil {
		return errors.New("memory: session metadata update is invalid")
	}

	directoryLock := s.directoryLock()
	directoryLock.Lock()
	defer directoryLock.Unlock()
	if pendingErr := s.pendingDeletionError(); pendingErr != nil {
		return pendingErr
	}
	resolvedKey, found, err := s.resolveSessionKeyLocked(sessionKey)
	if err != nil {
		return err
	}
	if found && resolvedKey != "" {
		sessionKey = resolvedKey
	}
	lock := s.sessionLock(sessionKey)
	lock.Lock()
	defer lock.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	previousKey := meta.Key
	previousSlot := meta.HistorySlot
	previousSkip := meta.Skip
	previousCount := meta.Count
	previousAliases := append([]string(nil), meta.Aliases...)
	if err := update(&meta); err != nil {
		return err
	}
	if previousKey != "" && meta.Key != previousKey {
		return errors.New("memory: session metadata update changed the canonical key")
	}
	if meta.HistorySlot != previousSlot || meta.Skip != previousSkip || meta.Count != previousCount {
		return errors.New("memory: session metadata update changed history-owned fields")
	}
	meta.Key = sessionKey
	meta.Scope = cloneRawJSON(meta.Scope)
	if slices.Equal(meta.Aliases, previousAliases) {
		meta.Aliases = append([]string(nil), previousAliases...)
	} else {
		meta.Aliases = normalizeAliases(sessionKey, meta.Aliases)
		if _, err := s.validateAliasOwnershipLocked(
			sessionKey,
			meta.Aliases,
			previousAliases,
			true,
		); err != nil {
			return err
		}
	}
	return s.writeMeta(sessionKey, meta)
}

// EnsureSessionHistory creates an empty legacy history file when a session
// has no selected slot. A missing selected slot is corruption and is never
// silently recreated.
func (s *JSONLStore) EnsureSessionHistory(ctx context.Context, sessionKey string) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return errors.New("memory: session key is empty")
	}
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return err
	}
	defer unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	if meta.Key != sessionKey {
		return fmt.Errorf(
			"memory: session metadata key %q does not match canonical key %q",
			meta.Key,
			sessionKey,
		)
	}
	if validationErr := validateMetaOffsets(meta); validationErr != nil {
		return validationErr
	}
	path, err := s.historyPath(sessionKey, meta)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("memory: stat history: %w", err)
	} else if meta.HistorySlot != "" {
		return fmt.Errorf("memory: active history slot %q is missing", meta.HistorySlot)
	} else if meta.Skip != 0 || meta.Count != 0 {
		return fmt.Errorf(
			"memory: missing legacy history has skip %d and count %d",
			meta.Skip,
			meta.Count,
		)
	}
	return s.hooks.writeHistory(path, nil, 0o644)
}

// PromoteAliasHistory atomically promotes the first non-empty alias session
// into the canonical session when the canonical session is still empty.
//
// Main-session aliases (e.g. "agent:main:main" or its opaque form) are
// skipped during promotion.  The main session is a shared global fallback
// and promoting its history into individual sessions would attach stale
// messages to every new Web UI session (issue #2972).
func (s *JSONLStore) PromoteAliasHistory(
	ctx context.Context,
	sessionKey string,
	scope json.RawMessage,
	aliases []string,
) (bool, error) {
	if ctxErr := contextError(ctx); ctxErr != nil {
		return false, ctxErr
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return false, nil
	}

	directoryLock := s.directoryLock()
	directoryLock.Lock()
	defer directoryLock.Unlock()
	if pendingErr := s.pendingDeletionError(); pendingErr != nil {
		return false, pendingErr
	}

	aliases = normalizeAliases(sessionKey, aliases)
	for _, alias := range aliases {
		if isMainSessionAlias(alias) {
			continue
		}
		unlock := s.lockSessionPair(sessionKey, alias)
		promoted, err := s.promoteAliasHistoryLocked(sessionKey, alias, scope, aliases)
		unlock()
		if err != nil || promoted {
			return promoted, err
		}
	}

	return false, nil
}

// isMainSessionAlias reports whether alias is the legacy or opaque main-session
// key.  The main session ("agent:<agent>:main") is a shared fallback and should
// not have its history promoted into individual per-channel sessions.
func isMainSessionAlias(alias string) bool {
	if alias == "" {
		return false
	}
	// Legacy form: "agent:main:main" (exactly 3 colon-separated parts)
	// Must not match "agent:sales:direct:main" etc.
	if strings.HasPrefix(alias, "agent:") && strings.HasSuffix(alias, ":main") {
		parts := strings.SplitN(alias, ":", 4)
		if len(parts) == 3 {
			return true
		}
	}
	// Opaque form: "sk_v1_" + SHA256("agent:main:main")
	if strings.HasPrefix(alias, "sk_v1_") {
		for _, agentID := range []string{"main", "Main", "MAIN"} {
			legacy := "agent:" + agentID + ":main"
			hash := sha256.Sum256([]byte(legacy))
			if "sk_v1_"+hex.EncodeToString(hash[:]) == alias {
				return true
			}
		}
	}
	return false
}

// ResolveSessionKey returns the canonical session key for a candidate key.
// It short-circuits direct canonical keys when possible, then scans metadata
// once to resolve aliases or canonical metadata keys.
func (s *JSONLStore) ResolveSessionKey(ctx context.Context, sessionKey string) (string, bool, error) {
	if err := contextError(ctx); err != nil {
		return "", false, err
	}
	directoryLock := s.directoryLock()
	directoryLock.RLock()
	defer directoryLock.RUnlock()
	if pendingErr := s.pendingDeletionError(); pendingErr != nil {
		return "", false, pendingErr
	}
	return s.resolveSessionKeyLocked(sessionKey)
}

func (s *JSONLStore) resolveSessionKeyLocked(sessionKey string) (string, bool, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return "", false, nil
	}

	hasDirectSession := s.sessionExists(sessionKey)
	if hasDirectSession && shouldShortCircuitSessionResolve(sessionKey) {
		return sessionKey, true, nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", false, fmt.Errorf("memory: read sessions dir: %w", err)
	}

	var directMetaMatch string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}

		data, readErr := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if readErr != nil {
			log.Printf("memory: skipping unreadable meta %s: %v", entry.Name(), readErr)
			continue
		}

		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			log.Printf("memory: skipping corrupt meta %s: %v", entry.Name(), err)
			continue
		}

		if meta.Key == "" {
			continue
		}

		if meta.Key == sessionKey {
			directMetaMatch = meta.Key
		}

		for _, alias := range meta.Aliases {
			if alias == sessionKey && meta.Key != sessionKey {
				return meta.Key, true, nil
			}
		}
	}

	if directMetaMatch != "" {
		return directMetaMatch, true, nil
	}

	if hasDirectSession {
		return sessionKey, true, nil
	}

	return "", false, nil
}

// lockResolvedSession resolves a legacy alias and acquires the canonical
// session lock while retaining the directory read lock. Alias ownership cannot
// change until the returned unlock function runs, so callers never act on a
// key whose mapping became stale between resolution and access. Ordinary store
// operations retain support for the historical blank key; strict snapshot,
// replacement, and deletion APIs validate nonblank keys separately.
func (s *JSONLStore) lockResolvedSession(
	ctx context.Context,
	sessionKey string,
) (string, func(), error) {
	if err := contextError(ctx); err != nil {
		return "", nil, err
	}
	sessionKey = strings.TrimSpace(sessionKey)

	directoryLock := s.directoryLock()
	directoryLock.RLock()
	if pendingErr := s.pendingDeletionError(); pendingErr != nil {
		directoryLock.RUnlock()
		return "", nil, pendingErr
	}
	requestedKey := sessionKey
	resolved, found, err := s.resolveSessionKeyLocked(sessionKey)
	if err != nil {
		directoryLock.RUnlock()
		return "", nil, err
	}
	if found && resolved != "" {
		sessionKey = resolved
	}
	if s.hooks.afterResolve != nil {
		s.hooks.afterResolve(requestedKey, sessionKey)
	}
	lock := s.sessionLock(sessionKey)
	lock.Lock()
	if err := contextError(ctx); err != nil {
		lock.Unlock()
		directoryLock.RUnlock()
		return "", nil, err
	}
	return sessionKey, func() {
		lock.Unlock()
		directoryLock.RUnlock()
	}, nil
}

// ReadSessionSnapshot returns an exact, coherent snapshot of an existing
// session. Alias discovery is strict: unreadable or malformed metadata is an
// error instead of being silently skipped. History, summary, and metadata are
// then read together while holding the canonical session lock.
//
// This method intentionally is not part of Store. It is an optional capability
// consumed by session.JSONLBackend when strict read-only invocation is needed.
func (s *JSONLStore) ReadSessionSnapshot(
	ctx context.Context,
	sessionKey string,
) (canonicalKey string, history []providers.Message, meta SessionMeta, found bool, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", nil, SessionMeta{}, false, ctxErr
	}
	if strings.TrimSpace(sessionKey) == "" {
		return "", nil, SessionMeta{}, false, nil
	}
	directoryLock := s.directoryLock()
	directoryLock.RLock()
	defer directoryLock.RUnlock()
	if pendingErr := s.pendingDeletionError(); pendingErr != nil {
		return "", nil, SessionMeta{}, false, pendingErr
	}

	canonicalKey, found, err = s.resolveSessionKeyStrict(ctx, sessionKey)
	if err != nil || !found {
		return canonicalKey, nil, SessionMeta{}, found, err
	}
	return s.readResolvedSessionSnapshot(ctx, sessionKey, canonicalKey)
}

// ReplaceSessionSnapshot atomically replaces history, summary, scope, and
// aliases using an opaque compare-and-swap revision. The inactive history file
// is durable before metadata selects it; therefore readers observe only the
// complete old tuple or the complete new tuple.
func (s *JSONLStore) ReplaceSessionSnapshot(
	ctx context.Context,
	replacement SessionSnapshotReplacement,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateSnapshotReplacement(replacement); err != nil {
		return err
	}

	directoryLock := s.directoryLock()
	directoryLock.Lock()
	defer directoryLock.Unlock()
	if pendingErr := s.pendingDeletionError(); pendingErr != nil {
		return pendingErr
	}
	lock := s.sessionLock(replacement.Key)
	lock.Lock()
	defer lock.Unlock()

	exists, err := s.sessionExistsStrict(replacement.Key)
	if err != nil {
		return err
	}
	if !exists {
		if replacement.ExpectedRevision != "" {
			return ErrSnapshotConflict
		}
	} else if replacement.ExpectedRevision == "" {
		return ErrSnapshotConflict
	}

	meta := SessionMeta{Key: replacement.Key}
	var existingAliases []string
	if exists {
		meta, err = s.readMetaStrict(replacement.Key)
		if err != nil {
			return err
		}
		if meta.Key != replacement.Key {
			return fmt.Errorf(
				"memory: session metadata key %q does not match canonical key %q",
				meta.Key,
				replacement.Key,
			)
		}
		historyPath, pathErr := s.committedHistoryPath(replacement.Key, meta)
		if pathErr != nil {
			return pathErr
		}
		currentHistory, readErr := readMessagesStrict(ctx, historyPath, meta)
		if readErr != nil {
			return readErr
		}
		currentRevision, revisionErr := snapshotRevision(replacement.Key, currentHistory, meta)
		if revisionErr != nil {
			return revisionErr
		}
		if currentRevision != replacement.ExpectedRevision {
			return ErrSnapshotConflict
		}
		existingAliases = append([]string(nil), meta.Aliases...)
	}

	aliases := normalizeAliases(replacement.Key, replacement.Aliases)
	if len(aliases) != len(replacement.Aliases) {
		return errors.New("memory: session snapshot aliases are not canonical")
	}
	for index := range aliases {
		if aliases[index] != replacement.Aliases[index] {
			return errors.New("memory: session snapshot aliases are not canonical")
		}
	}
	preservedDirectShadows, preservedAmbiguous, aliasErr := s.validateAliasesAvailableLocked(
		replacement.Key,
		aliases,
		existingAliases,
	)
	if aliasErr != nil {
		return aliasErr
	}

	slot, err := inactiveHistorySlot(meta.HistorySlot)
	if err != nil {
		return err
	}
	if _, pathErr := s.committedHistoryPath(replacement.Key, meta); pathErr != nil {
		return pathErr
	}
	historyData, err := encodeHistory(replacement.History)
	if err != nil {
		return err
	}
	if writeErr := s.hooks.writeHistory(
		s.historySlotPath(replacement.Key, slot),
		historyData,
		0o644,
	); writeErr != nil {
		return fmt.Errorf("memory: write session snapshot history: %w", writeErr)
	}
	if ctxErr := contextError(ctx); ctxErr != nil {
		return ctxErr
	}

	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Key = replacement.Key
	meta.HistorySlot = slot
	meta.Skip = 0
	meta.Count = len(replacement.History)
	meta.Summary = replacement.Summary
	meta.Scope = cloneRawJSON(replacement.Scope)
	meta.Aliases = append([]string(nil), aliases...)
	meta.UpdatedAt = now
	meta.Revision = ""
	if metaErr := s.writeMeta(replacement.Key, meta); metaErr != nil {
		return fmt.Errorf("memory: commit session snapshot metadata: %w", metaErr)
	}
	committedMeta, err := s.readMetaStrict(replacement.Key)
	if err != nil {
		return fmt.Errorf("memory: verify committed session snapshot metadata: %w", err)
	}
	if committedMeta.Key != replacement.Key || !slices.Equal(committedMeta.Aliases, aliases) {
		return errors.New("memory: committed session snapshot metadata did not persist exactly")
	}
	for _, alias := range aliases {
		if isMainSessionAlias(alias) {
			continue
		}
		if _, ambiguous := preservedAmbiguous[alias]; ambiguous {
			continue
		}
		if _, preserved := preservedDirectShadows[alias]; preserved {
			resolved, found, err := s.resolveSessionKeyStrictMode(ctx, alias, true)
			if err != nil {
				return fmt.Errorf("memory: verify preserved session snapshot alias %q: %w", alias, err)
			}
			if !found || resolved != replacement.Key {
				return fmt.Errorf(
					"memory: preserved session snapshot alias %q did not retain its owner",
					alias,
				)
			}
			continue
		}
		resolved, found, err := s.resolveSessionKeyStrict(ctx, alias)
		if err != nil {
			return fmt.Errorf("memory: verify session snapshot alias %q: %w", alias, err)
		}
		if !found || resolved != replacement.Key {
			return fmt.Errorf("memory: session snapshot alias %q did not persist exactly", alias)
		}
	}
	return nil
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
					index,
					blockIndex,
				)
			}
		}
		for callIndex, call := range message.ToolCalls {
			if call.Name != "" || call.Arguments != nil || call.ThoughtSignature != "" {
				return fmt.Errorf(
					"memory: session snapshot message %d tool call %d has non-persistable runtime fields",
					index,
					callIndex,
				)
			}
		}
	}
	if _, err := encodeHistory(replacement.History); err != nil {
		return err
	}
	return nil
}

// DeleteSession durably removes one exact session and all of its history files.
// A persisted deletion manifest makes an interrupted deletion recoverable
// before a reopened store serves data.
func (s *JSONLStore) DeleteSession(ctx context.Context, sessionKey string) (bool, error) {
	return s.DeleteSessions(ctx, []string{sessionKey})
}

// DeleteSessions durably deletes a related group, such as a canonical session
// plus retained promoted alias shadows, as one recoverable logical operation.
// The manifest is committed before any member is removed and cleared only
// after every removal is durable.
func (s *JSONLStore) DeleteSessions(ctx context.Context, sessionKeys []string) (bool, error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}
	keys, err := normalizeDeleteSessionKeys(sessionKeys)
	if err != nil {
		return false, err
	}
	directoryLock := s.directoryLock()
	directoryLock.Lock()
	defer directoryLock.Unlock()
	if recoverErr := s.recoverPendingDeletionsLocked(); recoverErr != nil {
		return false, recoverErr
	}
	return s.deleteSessionsLocked(ctx, keys)
}

// DeleteSessionsWithAliasesMatching discovers every current metadata-backed
// resource accepted by matchesMeta and selects matching alias shadows while
// holding the same directory write lock used to commit their grouped deletion.
// candidateKeys provide exact metadata-less resources found by a caller's
// legacy filename projection. matchesMeta receives whether metadata was present
// so it can distinguish those orphans from scoped resources. Both predicates
// must be short and non-reentrant.
func (s *JSONLStore) DeleteSessionsWithAliasesMatching(
	ctx context.Context,
	candidateKeys []string,
	matchesMeta func(SessionMeta, bool) bool,
	includeAlias func(SessionMeta, string) bool,
) (bool, error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}
	if matchesMeta == nil {
		return false, errors.New("memory: session deletion metadata predicate is nil")
	}
	candidateKeys, err := normalizeDeleteSessionKeys(candidateKeys)
	if err != nil {
		return false, err
	}
	directoryLock := s.directoryLock()
	directoryLock.Lock()
	defer directoryLock.Unlock()
	if recoverErr := s.recoverPendingDeletionsLocked(); recoverErr != nil {
		return false, recoverErr
	}

	metadata, err := s.readSessionMetadataCatalogLocked(ctx)
	if err != nil {
		return false, err
	}
	selected := make(map[string]struct{})
	matchedOwners := make([]SessionMeta, 0)
	for _, key := range sortedSessionMetaKeys(metadata) {
		meta := metadata[key]
		if matchesMeta(cloneSessionMeta(meta), true) {
			selected[key] = struct{}{}
			matchedOwners = append(matchedOwners, meta)
		}
	}
	for _, key := range candidateKeys {
		if _, metadataFound := metadata[key]; metadataFound {
			continue
		}
		// Sanitized filenames are not injective for legacy keys. A metadata
		// file at the candidate's physical path means it is not an orphan,
		// even when that file declares another colliding logical key.
		if _, statErr := os.Stat(s.metaPath(key)); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return false, fmt.Errorf("memory: stat candidate session metadata: %w", statErr)
		}
		if matchesMeta(SessionMeta{Key: key}, false) {
			selected[key] = struct{}{}
		}
	}

	if includeAlias != nil {
		for _, owner := range matchedOwners {
			for _, alias := range owner.Aliases {
				alias = strings.TrimSpace(alias)
				if alias == "" || !includeAlias(cloneSessionMeta(owner), alias) {
					continue
				}
				// Never let an owner's compatibility alias erase a current
				// metadata-backed resource that does not itself match the
				// deletion identity. A matching direct shadow is already (or
				// becomes) part of the same group.
				if direct, found := metadata[alias]; found {
					if !matchesMeta(cloneSessionMeta(direct), true) {
						continue
					}
				} else if _, statErr := os.Stat(s.metaPath(alias)); statErr == nil {
					// A different logical key owns this colliding physical path.
					continue
				} else if !os.IsNotExist(statErr) {
					return false, fmt.Errorf("memory: stat alias session metadata: %w", statErr)
				}
				selected[alias] = struct{}{}
			}
		}
	}
	if len(selected) == 0 {
		return false, nil
	}
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return s.deleteSessionsLocked(ctx, keys)
}

func (s *JSONLStore) readSessionMetadataCatalogLocked(
	ctx context.Context,
) (map[string]SessionMeta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("memory: read session metadata catalog: %w", err)
	}
	metadata := make(map[string]SessionMeta)
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("memory: read session metadata %s: %w", entry.Name(), err)
		}
		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("memory: decode session metadata %s: %w", entry.Name(), err)
		}
		meta.Key = strings.TrimSpace(meta.Key)
		if meta.Key == "" {
			return nil, fmt.Errorf("memory: decode session metadata %s: missing key", entry.Name())
		}
		if s.metaPath(meta.Key) != path {
			return nil, fmt.Errorf(
				"memory: session metadata key %q does not match file %s",
				meta.Key,
				entry.Name(),
			)
		}
		if _, exists := metadata[meta.Key]; exists {
			return nil, fmt.Errorf("memory: duplicate session metadata key %q", meta.Key)
		}
		metadata[meta.Key] = cloneSessionMeta(meta)
	}
	return metadata, nil
}

func sortedSessionMetaKeys(metadata map[string]SessionMeta) []string {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *JSONLStore) deleteSessionsLocked(ctx context.Context, keys []string) (bool, error) {
	unlockSessions := s.lockSessionKeys(keys)
	defer unlockSessions()

	found, err := s.validateDeleteTargetsLocked(keys)
	if err != nil || !found {
		return false, err
	}
	// No destructive intent is committed before this final cancellation check.
	// Once the manifest is durable, cleanup intentionally runs to completion
	// regardless of later cancellation and a cleanup error is commit-uncertain.
	if ctxErr := contextError(ctx); ctxErr != nil {
		return false, ctxErr
	}
	manifest := sessionDeleteManifest{Version: 1, Keys: keys}
	data, err := json.Marshal(manifest)
	if err != nil {
		return false, fmt.Errorf("memory: encode session deletion manifest: %w", err)
	}
	manifestPath := s.deleteManifestPath(keys)
	if writeErr := s.hooks.writeDeleteManifest(manifestPath, data, 0o600); writeErr != nil {
		visible, inspectErr := visibleDeleteManifestMatches(manifestPath, data)
		if inspectErr != nil {
			s.markPendingDeletion()
			return true, errors.Join(
				fmt.Errorf("memory: commit session deletion manifest: %w", writeErr),
				inspectErr,
			)
		}
		if !visible {
			return false, fmt.Errorf("memory: commit session deletion manifest: %w", writeErr)
		}
		// Atomic replacement may have made the exact manifest visible before
		// reporting a directory-sync error. From that point deletion intent is
		// committed in the live namespace, so finish it synchronously. The
		// durable removals below also provide the later directory sync needed
		// to settle the final state.
	}
	s.markPendingDeletion()
	if err := s.removeSessionDataLocked(keys); err != nil {
		return true, err
	}
	if err := s.hooks.removeFile(manifestPath); err != nil {
		return true, fmt.Errorf("memory: clear session deletion manifest: %w", err)
	}
	s.clearPendingDeletion()
	return true, nil
}

func visibleDeleteManifestMatches(path string, expected []byte) (bool, error) {
	actual, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memory: inspect uncertain session deletion manifest: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return false, errors.New("memory: uncertain session deletion manifest has unexpected content")
	}
	return true, nil
}

func normalizeDeleteSessionKeys(sessionKeys []string) ([]string, error) {
	if len(sessionKeys) == 0 {
		return nil, errors.New("memory: session deletion keys are empty")
	}
	keys := make([]string, 0, len(sessionKeys))
	seen := make(map[string]struct{}, len(sessionKeys))
	for _, key := range sessionKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("memory: session key is empty")
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *JSONLStore) deleteManifestPath(keys []string) string {
	digest := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	return filepath.Join(
		s.dir,
		deleteManifestPrefix+hex.EncodeToString(digest[:])+deleteManifestSuffix,
	)
}

func (s *JSONLStore) sessionDataPaths(key string) []string {
	return []string{
		s.metaPath(key),
		s.jsonlPath(key),
		s.historySlotPath(key, "a"),
		s.historySlotPath(key, "b"),
	}
}

func (s *JSONLStore) validateDeleteTargetsLocked(keys []string) (bool, error) {
	found := false
	for _, key := range keys {
		data, err := os.ReadFile(s.metaPath(key))
		switch {
		case err == nil:
			found = true
			var meta SessionMeta
			if decodeErr := json.Unmarshal(data, &meta); decodeErr != nil {
				return false, fmt.Errorf("memory: decode session metadata: %w", decodeErr)
			}
			if meta.Key != "" && meta.Key != key {
				return false, fmt.Errorf(
					"memory: session metadata key %q does not match canonical key %q",
					meta.Key,
					key,
				)
			}
		case os.IsNotExist(err):
		default:
			return false, fmt.Errorf("memory: read session metadata: %w", err)
		}
		for _, path := range s.sessionDataPaths(key)[1:] {
			if _, err := os.Stat(path); err == nil {
				found = true
			} else if !os.IsNotExist(err) {
				return false, fmt.Errorf("memory: stat session data: %w", err)
			}
		}
	}
	return found, nil
}

func (s *JSONLStore) removeSessionDataLocked(keys []string) error {
	for _, key := range keys {
		for _, path := range s.sessionDataPaths(key) {
			if err := s.hooks.removeFile(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("memory: remove session data: %w", err)
			}
		}
	}
	return nil
}

func (s *JSONLStore) recoverPendingDeletionsLocked() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("memory: read deletion manifests: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, deleteManifestPrefix) ||
			!strings.HasSuffix(name, deleteManifestSuffix) {
			continue
		}
		s.markPendingDeletion()
		manifestPath := filepath.Join(s.dir, name)
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return fmt.Errorf("memory: read session deletion manifest: %w", err)
		}
		var manifest sessionDeleteManifest
		if decodeErr := json.Unmarshal(data, &manifest); decodeErr != nil {
			return fmt.Errorf("memory: decode session deletion manifest: %w", decodeErr)
		}
		keys, err := normalizeDeleteSessionKeys(manifest.Keys)
		if err != nil || manifest.Version != 1 || !slices.Equal(keys, manifest.Keys) ||
			s.deleteManifestPath(keys) != manifestPath {
			return fmt.Errorf("memory: invalid session deletion manifest %s", name)
		}
		unlockSessions := s.lockSessionKeys(keys)
		if err := s.removeSessionDataLocked(keys); err != nil {
			unlockSessions()
			return fmt.Errorf("memory: recover session deletion: %w", err)
		}
		if err := s.hooks.removeFile(manifestPath); err != nil {
			unlockSessions()
			return fmt.Errorf("memory: clear recovered session deletion manifest: %w", err)
		}
		unlockSessions()
	}
	s.clearPendingDeletion()
	return nil
}

func (s *JSONLStore) lockSessionKeys(keys []string) func() {
	shards := make([]uint32, 0, len(keys))
	seen := make(map[uint32]struct{}, len(keys))
	for _, key := range keys {
		shard := s.sessionLockShard(key)
		if _, exists := seen[shard]; exists {
			continue
		}
		seen[shard] = struct{}{}
		shards = append(shards, shard)
	}
	sort.Slice(shards, func(i, j int) bool { return shards[i] < shards[j] })
	for _, shard := range shards {
		sharedSessionLocks[shard].Lock()
	}
	return func() {
		for index := len(shards) - 1; index >= 0; index-- {
			sharedSessionLocks[shards[index]].Unlock()
		}
	}
}

func (s *JSONLStore) readResolvedSessionSnapshot(
	ctx context.Context,
	requestedKey string,
	canonicalKey string,
) (resolvedKey string, history []providers.Message, meta SessionMeta, found bool, err error) {
	lock := s.sessionLock(canonicalKey)
	lock.Lock()
	defer lock.Unlock()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", nil, SessionMeta{}, false, ctxErr
	}
	exists, err := s.sessionExistsStrict(canonicalKey)
	if err != nil {
		return "", nil, SessionMeta{}, false, err
	}
	if !exists {
		return "", nil, SessionMeta{}, false, nil
	}

	meta, err = s.readMetaStrict(canonicalKey)
	if err != nil {
		return "", nil, SessionMeta{}, false, err
	}
	if meta.Key != canonicalKey {
		return "", nil, SessionMeta{}, false, fmt.Errorf(
			"memory: session metadata key %q does not match canonical key %q",
			meta.Key,
			canonicalKey,
		)
	}
	if requestedKey != canonicalKey && !slices.Contains(meta.Aliases, requestedKey) {
		return "", nil, SessionMeta{}, false, fmt.Errorf(
			"memory: session alias %q changed during snapshot lookup",
			requestedKey,
		)
	}
	historyPath, err := s.committedHistoryPath(canonicalKey, meta)
	if err != nil {
		return "", nil, SessionMeta{}, false, err
	}
	history, err = readMessagesStrict(ctx, historyPath, meta)
	if err != nil {
		return "", nil, SessionMeta{}, false, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", nil, SessionMeta{}, false, ctxErr
	}

	meta.Revision, err = snapshotRevision(canonicalKey, history, meta)
	if err != nil {
		return "", nil, SessionMeta{}, false, err
	}

	return canonicalKey, history, cloneSessionMeta(meta), true, nil
}

func (s *JSONLStore) resolveSessionKeyStrict(
	ctx context.Context,
	sessionKey string,
) (string, bool, error) {
	return s.resolveSessionKeyStrictMode(ctx, sessionKey, false)
}

func (s *JSONLStore) resolveSessionKeyStrictMode(
	ctx context.Context,
	sessionKey string,
	preferAliasOwner bool,
) (string, bool, error) {
	hasDirectSession, err := s.sessionExistsStrict(sessionKey)
	if err != nil {
		return "", false, err
	}
	if hasDirectSession && shouldShortCircuitSessionResolve(sessionKey) && !preferAliasOwner {
		return sessionKey, true, nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", false, fmt.Errorf("memory: read sessions dir: %w", err)
	}

	var directMetaMatch string
	var aliasMatch string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}

		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false, fmt.Errorf("memory: read session metadata %s: %w", entry.Name(), err)
		}

		var candidate SessionMeta
		if err := json.Unmarshal(data, &candidate); err != nil {
			return "", false, fmt.Errorf("memory: decode session metadata %s: %w", entry.Name(), err)
		}
		candidate.Key = strings.TrimSpace(candidate.Key)
		if candidate.Key == "" {
			return "", false, fmt.Errorf("memory: decode session metadata %s: missing key", entry.Name())
		}

		if candidate.Key == sessionKey {
			directMetaMatch = candidate.Key
		}
		for _, alias := range candidate.Aliases {
			if alias == sessionKey && candidate.Key != sessionKey {
				if aliasMatch != "" && aliasMatch != candidate.Key {
					return "", false, fmt.Errorf(
						"memory: session alias %q maps to multiple canonical keys",
						sessionKey,
					)
				}
				aliasMatch = candidate.Key
			}
		}
	}

	if directMetaMatch != "" {
		if aliasMatch != "" && aliasMatch != directMetaMatch {
			if preferAliasOwner {
				return aliasMatch, true, nil
			}
			return "", false, fmt.Errorf(
				"memory: session key %q is both canonical and an alias for another session",
				sessionKey,
			)
		}
		return directMetaMatch, true, nil
	}
	if aliasMatch != "" {
		return aliasMatch, true, nil
	}
	if hasDirectSession {
		return sessionKey, true, nil
	}
	return "", false, nil
}

func shouldShortCircuitSessionResolve(sessionKey string) bool {
	const opaqueSessionKeyPrefix = "sk_v1_"
	sessionKey = strings.TrimSpace(sessionKey)
	if len(sessionKey) != len(opaqueSessionKeyPrefix)+sha256.Size*2 ||
		!strings.HasPrefix(sessionKey, opaqueSessionKeyPrefix) {
		return false
	}
	for _, character := range sessionKey[len(opaqueSessionKeyPrefix):] {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *JSONLStore) orderedSessionLockShards(keyA, keyB string) (uint32, uint32) {
	shardA := s.sessionLockShard(keyA)
	shardB := s.sessionLockShard(keyB)
	// Pair ordering must use the process-global shard identity, not raw keys.
	// Stores rooted in different directories can map the same two raw keys to
	// opposite shards while holding different directory locks.
	if shardA > shardB {
		return shardB, shardA
	}
	return shardA, shardB
}

func (s *JSONLStore) lockSessionPair(keyA, keyB string) func() {
	first, second := s.orderedSessionLockShards(keyA, keyB)
	firstLock := &sharedSessionLocks[first]
	if first == second {
		firstLock.Lock()
		return func() { firstLock.Unlock() }
	}
	secondLock := &sharedSessionLocks[second]
	firstLock.Lock()
	secondLock.Lock()
	return func() {
		secondLock.Unlock()
		firstLock.Unlock()
	}
}

func (s *JSONLStore) promoteAliasHistoryLocked(
	sessionKey string,
	alias string,
	scope json.RawMessage,
	aliases []string,
) (bool, error) {
	canonicalMeta, err := s.readMeta(sessionKey)
	if err != nil {
		return false, err
	}
	canonicalHasContent, err := s.sessionHasVisibleContentLocked(sessionKey, canonicalMeta)
	if err != nil {
		return false, err
	}
	if canonicalHasContent {
		return false, nil
	}

	aliasMeta, err := s.readMeta(alias)
	if err != nil {
		return false, err
	}
	aliasPath, err := s.committedHistoryPath(alias, aliasMeta)
	if err != nil {
		return false, err
	}
	aliasHistory, err := readMessages(aliasPath, aliasMeta.Skip)
	if err != nil {
		return false, err
	}
	aliasSummary := strings.TrimSpace(aliasMeta.Summary)
	if len(aliasHistory) == 0 && aliasSummary == "" {
		return false, nil
	}

	now := time.Now()
	if canonicalMeta.CreatedAt.IsZero() {
		canonicalMeta.CreatedAt = now
	}
	canonicalMeta.Scope = cloneRawJSON(scope)
	canonicalMeta.Aliases = normalizeAliases(sessionKey, aliases)
	canonicalMeta.Skip = 0
	canonicalMeta.Count = len(aliasHistory)
	canonicalMeta.UpdatedAt = now
	if aliasSummary != "" {
		canonicalMeta.Summary = aliasSummary
	}

	if _, err := s.commitHistoryLocked(sessionKey, canonicalMeta, aliasHistory); err != nil {
		return false, err
	}
	return true, nil
}

func (s *JSONLStore) sessionHasVisibleContentLocked(sessionKey string, meta SessionMeta) (bool, error) {
	if strings.TrimSpace(meta.Summary) != "" {
		return true, nil
	}
	path, err := s.committedHistoryPath(sessionKey, meta)
	if err != nil {
		return false, err
	}
	history, err := readMessages(path, meta.Skip)
	if err != nil {
		return false, err
	}
	return len(history) > 0, nil
}

// readMessages reads valid JSON lines from a .jsonl file, skipping
// the first `skip` lines without unmarshaling them. This avoids the
// cost of json.Unmarshal on logically truncated messages.
// Malformed trailing lines (e.g. from a crash) are silently skipped.
func readMessages(path string, skip int) ([]providers.Message, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []providers.Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer f.Close()

	var msgs []providers.Message
	scanner := bufio.NewScanner(f)
	// Allow large lines for tool results (read_file, web search, etc.).
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	lineNum := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lineNum++
		if lineNum <= skip {
			continue
		}
		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			// Corrupt line — likely a partial write from a crash.
			// Log so operators know data was skipped, but don't
			// fail the entire read; this is the standard JSONL
			// recovery pattern.
			log.Printf("memory: skipping corrupt line %d in %s: %v",
				lineNum, filepath.Base(path), err)
			continue
		}
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}
		msgs = append(msgs, msg)
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("memory: scan jsonl: %w", scanner.Err())
	}

	if msgs == nil {
		msgs = []providers.Message{}
	}
	return msgs, nil
}

// readMessagesStrict is the snapshot-reader counterpart to readMessages. The
// normal recovery path tolerates malformed trailing records after a crash;
// strict existing-session reads must surface any corruption so the caller does
// not make a decision from silently incomplete context.
func readMessagesStrict(
	ctx context.Context,
	path string,
	meta SessionMeta,
) ([]providers.Message, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		if meta.HistorySlot == "" && meta.Skip == 0 && meta.Count == 0 {
			return []providers.Message{}, nil
		}
		return nil, fmt.Errorf(
			"memory: committed history is missing with skip %d and count %d",
			meta.Skip,
			meta.Count,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer f.Close()

	messages := make([]providers.Message, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	lineNumber := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lineNumber++
		if lineNumber <= meta.Skip {
			continue
		}

		var message providers.Message
		if err := json.Unmarshal(line, &message); err != nil {
			return nil, fmt.Errorf(
				"memory: decode jsonl line %d in %s: %w",
				lineNumber,
				filepath.Base(path),
				err,
			)
		}
		if messageutil.IsTransientAssistantThoughtMessage(message) {
			continue
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("memory: scan jsonl: %w", err)
	}
	if lineNumber < meta.Count {
		return nil, fmt.Errorf(
			"memory: session metadata count %d exceeds %d committed history records",
			meta.Count,
			lineNumber,
		)
	}
	return messages, nil
}

// scanRetainedMessageLines returns the total number of non-empty raw JSONL
// lines plus the raw line numbers that survive readMessages filtering.
// TruncateHistory uses this to compute keepLast against retained messages
// while preserving the raw-line skip offset stored in metadata.
func scanRetainedMessageLines(path string) (int, []int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, []int{}, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer f.Close()

	rawCount := 0
	retained := make([]int, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		rawCount++

		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}
		retained = append(retained, rawCount)
	}
	if err := scanner.Err(); err != nil {
		return 0, nil, err
	}
	return rawCount, retained, nil
}

func (s *JSONLStore) AddMessage(
	ctx context.Context, sessionKey, role, content string,
) error {
	return s.addMsg(ctx, sessionKey, providers.Message{
		Role:    role,
		Content: content,
	})
}

func (s *JSONLStore) AddFullMessage(
	ctx context.Context, sessionKey string, msg providers.Message,
) error {
	return s.addMsg(ctx, sessionKey, msg)
}

// addMsg is the shared implementation for AddMessage and AddFullMessage.
func (s *JSONLStore) addMsg(
	ctx context.Context,
	sessionKey string,
	msg providers.Message,
) error {
	if messageutil.IsTransientAssistantThoughtMessage(msg) {
		return nil
	}
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return err
	}
	defer unlock()

	now := time.Now()

	if msg.CreatedAt == nil {
		msg.CreatedAt = &now
	}
	meta := SessionMeta{Key: sessionKey}
	if _, statErr := os.Stat(s.metaPath(sessionKey)); statErr == nil {
		meta, err = s.readMetaStrict(sessionKey)
		if err != nil {
			return err
		}
		if meta.Key != sessionKey {
			return fmt.Errorf(
				"memory: session metadata key %q does not match canonical key %q",
				meta.Key,
				sessionKey,
			)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("memory: stat session metadata: %w", statErr)
	} else {
		rawCount, _, scanErr := scanRetainedMessageLines(s.jsonlPath(sessionKey))
		if scanErr != nil {
			return fmt.Errorf("memory: validate legacy history before append: %w", scanErr)
		}
		meta.Count = rawCount
	}
	historyPath, err := s.committedHistoryPath(sessionKey, meta)
	if err != nil {
		return err
	}

	// Append the message as a single JSON line.
	line, err := encodeJSONLMessage(msg)
	if err != nil {
		return fmt.Errorf("memory: encode message: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(
		historyPath,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return fmt.Errorf("memory: open jsonl for append: %w", err)
	}
	_, writeErr := f.Write(line)
	if writeErr != nil {
		f.Close()
		return fmt.Errorf("memory: append message: %w", writeErr)
	}
	// Flush to physical storage before closing. This matches the
	// durability guarantee of writeMeta and rewriteJSONL (which use
	// WriteFileAtomic with fsync). Without Sync, a power loss could
	// leave the append in the kernel page cache only — lost on reboot.
	if syncErr := f.Sync(); syncErr != nil {
		f.Close()
		return fmt.Errorf("memory: sync jsonl: %w", syncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("memory: close jsonl: %w", closeErr)
	}

	// Update metadata.
	if meta.Count == 0 && meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Count++
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

func (s *JSONLStore) GetHistory(
	ctx context.Context, sessionKey string,
) ([]providers.Message, error) {
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	defer unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return nil, err
	}

	// Pass meta.Skip so readMessages skips those lines without
	// unmarshaling them — avoids wasted CPU on truncated messages.
	path, err := s.committedHistoryPath(sessionKey, meta)
	if err != nil {
		return nil, err
	}
	msgs, err := readMessages(path, meta.Skip)
	if err != nil {
		return nil, err
	}

	return msgs, nil
}

func (s *JSONLStore) GetSummary(
	ctx context.Context, sessionKey string,
) (string, error) {
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return "", err
	}
	defer unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return "", err
	}
	return meta.Summary, nil
}

func (s *JSONLStore) SetSummary(
	ctx context.Context, sessionKey, summary string,
) error {
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return err
	}
	defer unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Summary = summary
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

func (s *JSONLStore) TruncateHistory(
	ctx context.Context, sessionKey string, keepLast int,
) error {
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return err
	}
	defer unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}

	path, err := s.committedHistoryPath(sessionKey, meta)
	if err != nil {
		return err
	}
	rawCount, retainedRawLines, scanErr := scanRetainedMessageLines(path)
	if scanErr != nil {
		return scanErr
	}
	meta.Count = rawCount
	if meta.Skip > meta.Count {
		meta.Skip = meta.Count
	}

	activeStart := sort.Search(len(retainedRawLines), func(i int) bool {
		return retainedRawLines[i] > meta.Skip
	})
	activeRetainedCount := len(retainedRawLines) - activeStart

	switch {
	case keepLast <= 0 || activeRetainedCount == 0:
		meta.Skip = meta.Count
	case keepLast < activeRetainedCount:
		activeRawLines := retainedRawLines[activeStart:]
		meta.Skip = activeRawLines[activeRetainedCount-keepLast-1]
	}
	meta.UpdatedAt = time.Now()

	return s.writeMeta(sessionKey, meta)
}

func (s *JSONLStore) SetHistory(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return err
	}
	defer unlock()
	history = messageutil.FilterInvalidHistoryMessages(history)

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Skip = 0
	meta.Count = len(history)
	meta.UpdatedAt = now

	for i := range history {
		if history[i].CreatedAt == nil {
			history[i].CreatedAt = &now
		}
	}

	_, err = s.commitHistoryLocked(sessionKey, meta, history)
	return err
}

// Compact physically rewrites the JSONL file, dropping all logically
// skipped lines. This reclaims disk space that accumulates after
// repeated TruncateHistory calls.
//
// It is safe to call at any time; if there is nothing to compact
// (skip == 0) the method returns immediately.
func (s *JSONLStore) Compact(
	ctx context.Context, sessionKey string,
) error {
	sessionKey, unlock, err := s.lockResolvedSession(ctx, sessionKey)
	if err != nil {
		return err
	}
	defer unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	path, err := s.committedHistoryPath(sessionKey, meta)
	if err != nil {
		return err
	}
	if meta.Skip == 0 {
		return nil
	}

	// Read only the active messages, skipping truncated lines
	// without unmarshaling them.
	active, err := readMessages(path, meta.Skip)
	if err != nil {
		return err
	}

	meta.Skip = 0
	meta.Count = len(active)
	meta.UpdatedAt = time.Now()
	_, err = s.commitHistoryLocked(sessionKey, meta, active)
	return err
}

// commitHistoryLocked writes a complete tuple to the inactive history slot,
// then flips metadata. The metadata rename is the sole visibility point.
func (s *JSONLStore) commitHistoryLocked(
	sessionKey string,
	meta SessionMeta,
	messages []providers.Message,
) (SessionMeta, error) {
	if _, err := s.committedHistoryPath(sessionKey, meta); err != nil {
		return SessionMeta{}, err
	}
	slot, err := inactiveHistorySlot(meta.HistorySlot)
	if err != nil {
		return SessionMeta{}, err
	}
	data, err := encodeHistory(messages)
	if err != nil {
		return SessionMeta{}, err
	}
	if err := s.hooks.writeHistory(s.historySlotPath(sessionKey, slot), data, 0o644); err != nil {
		return SessionMeta{}, fmt.Errorf("memory: write history slot: %w", err)
	}
	meta.HistorySlot = slot
	meta.Revision = ""
	if err := s.writeMeta(sessionKey, meta); err != nil {
		return SessionMeta{}, err
	}
	return meta, nil
}

func encodeHistory(messages []providers.Message) ([]byte, error) {
	var buf bytes.Buffer
	for index, message := range messages {
		line, err := encodeJSONLMessage(message)
		if err != nil {
			return nil, fmt.Errorf("memory: encode message %d: %w", index, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func encodeJSONLMessage(message providers.Message) ([]byte, error) {
	line, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	// Scanner needs one byte for the newline within its maximum token buffer.
	// Reject an unreadable record before any history file changes.
	if len(line) >= maxLineSize {
		return nil, fmt.Errorf(
			"encoded message exceeds maximum JSONL line size of %d bytes",
			maxLineSize-1,
		)
	}
	return line, nil
}

// rewriteJSONL atomically replaces the JSONL file with the given messages
// using the project's standard WriteFileAtomic (temp + fsync + rename).
func (s *JSONLStore) rewriteJSONL(
	sessionKey string, msgs []providers.Message,
) error {
	msgs = messageutil.FilterInvalidHistoryMessages(msgs)
	data, err := encodeHistory(msgs)
	if err != nil {
		return err
	}
	return s.hooks.writeHistory(s.jsonlPath(sessionKey), data, 0o644)
}

// ListSessions returns all known session keys by reading .meta.json files.
func (s *JSONLStore) ListSessions() []string {
	directoryLock := s.directoryLock()
	directoryLock.RLock()
	defer directoryLock.RUnlock()
	if s.pendingDeletionError() != nil {
		return nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var keys []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		// Read the meta file to get the original key
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.Key != "" {
			keys = append(keys, meta.Key)
		}
	}
	return keys
}

func (s *JSONLStore) Close() error {
	return nil
}
