//nolint:govet // Ordered importer phases intentionally reuse short-lived error bindings.
package memory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	legacySessionsArchiveVersion = "sessions-v1"
	legacySessionsMaxSourceBytes = int64(64 << 20)
	legacySessionsMaxSources     = 100_000
	legacySessionsMaxTotalBytes  = int64(1 << 30)
)

type capturedLegacySessionSource struct {
	input sqlitestore.LegacyInput
}

type jsonSession struct {
	Key      string              `json:"key"`
	Messages []providers.Message `json:"messages"`
	Summary  string              `json:"summary,omitempty"`
	Created  time.Time           `json:"created"`
	Updated  time.Time           `json:"updated"`
}

type legacyThreadMeta struct {
	ID                string               `json:"id"`
	UISessionID       string               `json:"ui_session_id"`
	PrimarySessionKey string               `json:"primary_session_key"`
	AgentID           string               `json:"agent_id"`
	OwnerIdentity     string               `json:"owner_identity"`
	Title             string               `json:"title"`
	Type              string               `json:"type"`
	Context           map[string]string    `json:"context,omitempty"`
	SourceQuery       string               `json:"source_query,omitempty"`
	SessionKeys       []string             `json:"session_keys"`
	Aliases           []string             `json:"aliases,omitempty"`
	Registration      string               `json:"registration"`
	DroppedAt         *time.Time           `json:"dropped_at,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
	AttachedAt        map[string]time.Time `json:"-"`
}

type legacyThreadHandoff struct {
	ID               string    `json:"id"`
	OriginSessionKey string    `json:"origin_session_key"`
	OriginSessionID  string    `json:"origin_session_id,omitempty"`
	TargetThreadID   string    `json:"target_thread_id"`
	TargetSessionID  string    `json:"target_session_id"`
	AgentID          string    `json:"agent_id"`
	Summary          string    `json:"summary,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

func newSessionsLegacyOptions(sessionsDir string) (*sqlitestore.LegacyOptions, error) {
	workspace := filepath.Dir(sessionsDir)
	workspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return nil, fmt.Errorf("memory: resolve legacy workspace: %w", err)
	}
	// A fresh workspace has nothing to migrate. Avoid subjecting its workspace
	// root to the stricter legacy-source mode checks; those checks are required
	// only when PicoClaw is about to trust and archive legacy input.
	initialSources, err := enumerateLegacySessionSources(workspace)
	if err != nil {
		return nil, fmt.Errorf("memory: enumerate legacy session sources: %w", err)
	}
	captured := make(map[string]capturedLegacySessionSource)
	options := &sqlitestore.LegacyOptions{
		SourceRoot:    workspace,
		ArchiveRoot:   filepath.Join(workspace, "legacy-json", legacySessionsArchiveVersion),
		MaxBytes:      legacySessionsMaxSourceBytes,
		MaxSources:    legacySessionsMaxSources,
		MaxTotalBytes: legacySessionsMaxTotalBytes,
	}
	options.Sources = func() ([]sqlitestore.LegacySource, error) {
		return append([]sqlitestore.LegacySource(nil), initialSources...), nil
	}
	options.Import = func(
		ctx context.Context,
		_ *sql.Conn,
		input sqlitestore.LegacyInput,
	) (sqlitestore.ImportResult, error) {
		if err := ctx.Err(); err != nil {
			return sqlitestore.ImportResult{}, err
		}
		captured[input.ID] = capturedLegacySessionSource{input: sqlitestore.LegacyInput{
			ID: input.ID, Relative: input.Relative, Data: append([]byte(nil), input.Data...),
			Digest: input.Digest, Limit: input.Limit, Mode: input.Mode, ModTime: input.ModTime,
		}}
		return sqlitestore.ImportResult{}, nil
	}
	options.FinalizeResults = func(
		ctx context.Context,
		conn *sql.Conn,
		input sqlitestore.LegacyFinalizeInput,
	) (map[string]sqlitestore.ImportResult, error) {
		ordered := make([]sqlitestore.LegacyInput, 0, len(input.SourceIDs))
		for _, sourceID := range input.SourceIDs {
			capturedSource, ok := captured[sourceID]
			if !ok {
				return nil, fmt.Errorf("captured source %s is missing", sourceID)
			}
			ordered = append(ordered, capturedSource.input)
		}
		return finalizeLegacySessions(ctx, conn, ordered)
	}
	return options, nil
}

func enumerateLegacySessionSources(workspace string) ([]sqlitestore.LegacySource, error) {
	var sources []sqlitestore.LegacySource
	for _, top := range []string{"sessions", "threads"} {
		root := filepath.Join(workspace, top)
		rootInfo, statErr := os.Lstat(root)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		if err := validateLegacyEnumerationDirectory(rootInfo); err != nil {
			return nil, fmt.Errorf("memory: unsafe legacy %s root: %w", top, err)
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) && path == root {
					return filepath.SkipDir
				}
				return walkErr
			}
			if path == root {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("legacy enumeration contains a symlink")
			}
			relative, err := filepath.Rel(workspace, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if entry.IsDir() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if err := validateLegacyEnumerationDirectory(info); err != nil {
					return err
				}
				return nil
			}
			if !isLegacySessionRelative(relative) {
				return nil
			}
			digest := sha256.Sum256([]byte(relative))
			sources = append(sources, sqlitestore.LegacySource{
				ID:       "session-source-" + hex.EncodeToString(digest[:]),
				Relative: relative,
				MaxBytes: legacySessionsMaxSourceBytes,
			})
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Relative < sources[j].Relative })
	return sources, nil
}

func validateLegacyEnumerationDirectory(info os.FileInfo) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		info.Mode().Perm()&0o022 != 0 {
		return errors.New("legacy directory must be a non-writable real directory")
	}
	return nil
}

func isLegacySessionRelative(relative string) bool {
	base := filepath.Base(filepath.FromSlash(relative))
	if relative == "sessions/"+SessionsDatabaseFilename ||
		strings.HasPrefix(relative, "sessions/"+SessionsDatabaseFilename+"-") {
		return false
	}
	if strings.HasPrefix(relative, "sessions/") {
		return strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".json.migrated") ||
			strings.HasSuffix(base, ".jsonl") || strings.HasSuffix(base, ".history-a") ||
			strings.HasSuffix(base, ".history-b")
	}
	if strings.HasPrefix(relative, "threads/") {
		return strings.HasSuffix(base, ".json")
	}
	return false
}

type legacySessionImportAudit struct {
	results map[string]sqlitestore.ImportResult
}

func newLegacySessionImportAudit(sources []sqlitestore.LegacyInput) *legacySessionImportAudit {
	audit := &legacySessionImportAudit{results: make(map[string]sqlitestore.ImportResult, len(sources))}
	for _, source := range sources {
		audit.results[source.ID] = sqlitestore.ImportResult{}
	}
	return audit
}

func (a *legacySessionImportAudit) imported(source sqlitestore.LegacyInput, count int) {
	result := a.results[source.ID]
	result.Imported += count
	a.results[source.ID] = result
}

func (a *legacySessionImportAudit) skipped(
	source sqlitestore.LegacyInput,
	code string,
	record []byte,
	count int,
) {
	if count <= 0 {
		return
	}
	result := a.results[source.ID]
	result.Skipped += count
	if len(result.Issues) < 512 {
		result.Issues = append(result.Issues, sqlitestore.ImportIssue{
			Code: code, RecordDigest: sha256.Sum256(record),
		})
	}
	a.results[source.ID] = result
}

type legacyThreadCandidate struct {
	meta   legacyThreadMeta
	source *sqlitestore.LegacyInput
}

func finalizeLegacySessions(
	ctx context.Context,
	conn *sql.Conn,
	sources []sqlitestore.LegacyInput,
) (map[string]sqlitestore.ImportResult, error) {
	audit := newLegacySessionImportAudit(sources)
	byRelative := make(map[string]sqlitestore.LegacyInput, len(sources))
	for _, source := range sources {
		byRelative[source.Relative] = source
	}
	deleted := make(map[string]struct{})
	for _, source := range sources {
		if !strings.HasPrefix(filepath.Base(source.Relative), deleteManifestPrefix) {
			continue
		}
		var manifest sessionDeleteManifest
		if err := decodeSingleLegacyJSON(source.Data, &manifest); err != nil || manifest.Version != 1 {
			audit.skipped(source, "invalid-delete-manifest", source.Data, 1)
			continue
		}
		keys, err := normalizeDeleteKeys(manifest.Keys)
		if err != nil {
			audit.skipped(source, "invalid-delete-manifest", source.Data, 1)
			continue
		}
		audit.imported(source, len(keys))
		for _, key := range keys {
			deleted[key] = struct{}{}
		}
	}

	metaSources := make([]sqlitestore.LegacyInput, 0)
	aggregateSources := make([]sqlitestore.LegacyInput, 0)
	historySources := make([]sqlitestore.LegacyInput, 0)
	threadSources := make([]sqlitestore.LegacyInput, 0)
	handoffSources := make([]sqlitestore.LegacyInput, 0)
	for _, source := range sources {
		relative := source.Relative
		base := filepath.Base(filepath.FromSlash(relative))
		switch {
		case strings.HasPrefix(relative, "sessions/") && strings.HasSuffix(base, ".meta.json"):
			metaSources = append(metaSources, source)
		case strings.HasPrefix(relative, "sessions/") &&
			(strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".json.migrated")) &&
			!strings.HasPrefix(base, deleteManifestPrefix):
			aggregateSources = append(aggregateSources, source)
		case strings.HasPrefix(relative, "sessions/") &&
			(strings.HasSuffix(base, ".jsonl") || strings.HasSuffix(base, ".history-a") ||
				strings.HasSuffix(base, ".history-b")):
			historySources = append(historySources, source)
		case strings.HasPrefix(relative, "threads/handoffs/"):
			handoffSources = append(handoffSources, source)
		case strings.HasPrefix(relative, "threads/"):
			threadSources = append(threadSources, source)
		}
	}
	claimedHistory := make(map[string]struct{})
	metaBackedHistory := make(map[string]struct{})
	legacyThreadByID := make(map[string]legacyThreadCandidate)
	for _, source := range metaSources {
		base := strings.TrimSuffix(source.Relative, ".meta.json")
		for _, suffix := range []string{".jsonl", ".history-a", ".history-b"} {
			claimedHistory[base+suffix] = struct{}{}
		}
	}
	for _, source := range metaSources {
		var meta SessionMeta
		if err := decodeSingleLegacyJSON(source.Data, &meta); err != nil {
			audit.skipped(source, "invalid-session-metadata", source.Data, 1)
			continue
		}
		var err error
		meta, err = prepareLegacySessionMeta(meta)
		if err != nil {
			audit.skipped(source, "invalid-session-metadata", source.Data, 1)
			continue
		}
		if _, removing := deleted[meta.Key]; removing {
			audit.skipped(source, "deleted-session", source.Data, 1)
			continue
		}
		base := strings.TrimSuffix(source.Relative, ".meta.json")
		historyRelative := base + ".jsonl"
		if meta.HistorySlot == "a" || meta.HistorySlot == "b" {
			historyRelative = base + ".history-" + meta.HistorySlot
		} else if meta.HistorySlot != "" {
			audit.skipped(source, "invalid-history-selector", source.Data, 1)
			continue
		}
		metaBackedHistory[historyRelative] = struct{}{}
		historyInput, found := byRelative[historyRelative]
		if !found && (meta.Count != 0 || meta.Skip != 0) {
			audit.skipped(source, "missing-selected-history", source.Data, 1)
			continue
		}
		if found {
			physicalCount, err := countLegacyHistoryRecords(historyInput.Data)
			if err != nil {
				return nil, err
			}
			if physicalCount < meta.Count {
				audit.skipped(source, "inconsistent-history-count", source.Data, 1)
				continue
			}
		}
		history, validHistoryCount, err := decodeLegacyHistoryForImport(
			historyInput, meta.Skip, audit,
		)
		if err != nil {
			return nil, err
		}
		if meta.CreatedAt.IsZero() {
			meta.CreatedAt = historyInput.ModTime
		}
		if meta.UpdatedAt.IsZero() {
			meta.UpdatedAt = historyInput.ModTime
		}
		inserted, err := insertLegacySession(ctx, conn, meta.Key, history, meta)
		if err != nil {
			return nil, err
		}
		if inserted {
			audit.imported(source, 1)
			if found {
				audit.imported(historyInput, validHistoryCount)
			}
		} else {
			audit.skipped(source, "session-identity-conflict", source.Data, 1)
			if found {
				audit.skipped(
					historyInput, "session-identity-conflict", historyInput.Data, validHistoryCount,
				)
			}
		}
		if inserted && strings.TrimSpace(meta.ThreadID) != "" {
			candidate, exists := legacyThreadByID[meta.ThreadID]
			if !exists {
				candidate.meta = legacyThreadMeta{
					ID: meta.ThreadID, UISessionID: meta.ThreadID, PrimarySessionKey: meta.Key,
					Title: meta.ThreadTitle, Type: meta.ThreadType, Context: meta.ThreadContext,
					SourceQuery: meta.ThreadSourceQuery, SessionKeys: []string{meta.Key},
					Registration: "tool", CreatedAt: meta.CreatedAt, UpdatedAt: meta.UpdatedAt,
				}
			}
			candidate.meta.SessionKeys = uniqueLegacyStrings(append(candidate.meta.SessionKeys, meta.Key))
			if candidate.meta.AttachedAt == nil {
				candidate.meta.AttachedAt = make(map[string]time.Time)
			}
			candidate.meta.AttachedAt[meta.Key] = meta.ThreadAttachedAt
			legacyThreadByID[meta.ThreadID] = candidate
		}
	}
	for _, source := range aggregateSources {
		var session jsonSession
		if err := decodeSingleLegacyJSON(source.Data, &session); err != nil {
			audit.skipped(source, "invalid-session-json", source.Data, 1)
			continue
		}
		key := strings.TrimSpace(session.Key)
		if key == "" {
			name := filepath.Base(source.Relative)
			key = strings.TrimSuffix(strings.TrimSuffix(name, ".migrated"), ".json")
		}
		if _, removing := deleted[key]; removing {
			audit.skipped(source, "deleted-session", source.Data, 1)
			continue
		}
		meta, err := prepareLegacySessionMeta(SessionMeta{
			Key: key, Summary: session.Summary, CreatedAt: session.Created, UpdatedAt: session.Updated,
		})
		if err != nil {
			audit.skipped(source, "invalid-session-record", source.Data, 1)
			continue
		}
		history := filterAggregateLegacyMessages(session.Messages, source, audit)
		inserted, err := insertLegacySession(ctx, conn, key, history, meta)
		if err != nil {
			return nil, err
		}
		if inserted {
			audit.imported(source, 1)
		} else {
			audit.skipped(source, "session-identity-conflict", source.Data, 1)
		}
	}
	for _, source := range historySources {
		if _, backed := metaBackedHistory[source.Relative]; backed {
			continue
		}
		base := filepath.Base(source.Relative)
		if _, claimed := claimedHistory[source.Relative]; claimed ||
			strings.HasSuffix(base, ".history-a") || strings.HasSuffix(base, ".history-b") {
			_, validCount, err := decodeLegacyHistoryForImport(source, 0, audit)
			if err != nil {
				return nil, err
			}
			audit.skipped(source, "inactive-or-orphan-history", source.Data, validCount)
			continue
		}
		key := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".jsonl"), ".history-a"), ".history-b")
		key = legacySessionKeyFromSanitizedBase(key)
		if _, removing := deleted[key]; removing {
			_, validCount, err := decodeLegacyHistoryForImport(source, 0, audit)
			if err != nil {
				return nil, err
			}
			audit.skipped(source, "deleted-session", source.Data, validCount)
			continue
		}
		history, validCount, err := decodeLegacyHistoryForImport(source, 0, audit)
		if err != nil {
			return nil, err
		}
		meta, err := prepareLegacySessionMeta(SessionMeta{
			Key: key, CreatedAt: source.ModTime, UpdatedAt: source.ModTime,
		})
		if err != nil {
			audit.skipped(source, "invalid-session-record", source.Data, max(1, validCount))
			continue
		}
		inserted, err := insertLegacySession(ctx, conn, key, history, meta)
		if err != nil {
			return nil, err
		}
		if inserted {
			audit.imported(source, validCount)
		} else {
			audit.skipped(source, "session-identity-conflict", source.Data, validCount)
		}
	}
	for _, source := range threadSources {
		var thread legacyThreadMeta
		if err := decodeSingleLegacyJSON(source.Data, &thread); err != nil ||
			validateLegacyThreadMeta(&thread) != nil {
			audit.skipped(source, "invalid-thread-record", source.Data, 1)
			continue
		}
		if _, exists := legacyThreadByID[thread.ID]; exists {
			audit.skipped(source, "thread-identity-conflict", source.Data, 1)
			continue
		}
		copySource := source
		legacyThreadByID[thread.ID] = legacyThreadCandidate{meta: thread, source: &copySource}
	}
	threadIDs := make([]string, 0, len(legacyThreadByID))
	for id := range legacyThreadByID {
		threadIDs = append(threadIDs, id)
	}
	sort.Strings(threadIDs)
	for _, id := range threadIDs {
		candidate := legacyThreadByID[id]
		inserted, reason, err := insertLegacyThread(ctx, conn, candidate.meta)
		if err != nil {
			return nil, err
		}
		if candidate.source != nil {
			if inserted {
				audit.imported(*candidate.source, 1)
			} else {
				audit.skipped(*candidate.source, reason, candidate.source.Data, 1)
			}
		}
	}
	for _, source := range handoffSources {
		var handoff legacyThreadHandoff
		if err := decodeSingleLegacyJSON(source.Data, &handoff); err != nil ||
			validateLegacyHandoff(&handoff) != nil {
			audit.skipped(source, "invalid-handoff-record", source.Data, 1)
			continue
		}
		inserted, reason, err := insertLegacyHandoff(ctx, conn, handoff)
		if err != nil {
			return nil, err
		}
		if inserted {
			audit.imported(source, 1)
		} else {
			audit.skipped(source, reason, source.Data, 1)
		}
	}
	for key := range deleted {
		if _, err := conn.ExecContext(ctx, `DELETE FROM sessions WHERE session_key = ?`, key); err != nil {
			return nil, err
		}
	}
	return audit.results, nil
}

func countLegacyHistoryRecords(data []byte) (int, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	count := 0
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) != 0 {
			count++
		}
	}
	return count, scanner.Err()
}

func decodeSingleLegacyJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("legacy JSON has trailing data")
		}
		return err
	}
	return nil
}

func prepareLegacySessionMeta(meta SessionMeta) (SessionMeta, error) {
	meta.Key = strings.TrimSpace(meta.Key)
	if !legacyTextBytesBetween(meta.Key, 1, 16_384) ||
		!legacyTextBytesBetween(meta.Summary, 0, 16_777_216) ||
		meta.Skip < 0 || meta.Count < 0 || meta.Skip > meta.Count ||
		!validLegacyTime(meta.CreatedAt) || !validLegacyTime(meta.UpdatedAt) ||
		!validLegacyTime(meta.ThreadAttachedAt) {
		return SessionMeta{}, errors.New("legacy session metadata is out of bounds")
	}
	if meta.HistorySlot != "" && meta.HistorySlot != "a" && meta.HistorySlot != "b" {
		return SessionMeta{}, errors.New("legacy session history selector is invalid")
	}
	aliases := normalizeAliases(meta.Key, meta.Aliases)
	for _, alias := range aliases {
		if !legacyTextBytesBetween(alias, 1, 16_384) {
			return SessionMeta{}, errors.New("legacy session alias is out of bounds")
		}
	}
	meta.Aliases = aliases
	if len(meta.Scope) > 0 {
		canonical, err := canonicalSessionScopeJSON(meta.Scope)
		if err != nil {
			return SessionMeta{}, err
		}
		var scope sqliteSessionScope
		if err := json.Unmarshal(canonical, &scope); err != nil || scope.Version < 0 ||
			!legacyTextBytesBetween(scope.AgentID, 0, 16_384) ||
			!legacyTextBytesBetween(scope.Channel, 0, 16_384) ||
			!legacyTextBytesBetween(scope.Account, 0, 16_384) {
			return SessionMeta{}, errors.New("legacy session scope is invalid")
		}
		seen := make(map[string]struct{}, len(scope.Dimensions))
		for _, dimension := range scope.Dimensions {
			value, ok := scope.Values[dimension]
			if !ok || !legacyTextBytesBetween(dimension, 1, 256) ||
				!legacyTextBytesBetween(value, 1, 16_384) {
				return SessionMeta{}, errors.New("legacy session scope dimension is invalid")
			}
			if _, duplicate := seen[dimension]; duplicate {
				return SessionMeta{}, errors.New("legacy session scope dimension is duplicated")
			}
			seen[dimension] = struct{}{}
		}
		for dimension, value := range scope.Values {
			if !legacyTextBytesBetween(dimension, 1, 256) ||
				!legacyTextBytesBetween(value, 1, 16_384) {
				return SessionMeta{}, errors.New("legacy session scope value is invalid")
			}
		}
		meta.Scope = canonical
	}
	if !legacyTextBytesBetween(meta.ThreadID, 0, 16_384) ||
		!legacyTextBytesBetween(meta.ThreadType, 0, 256) ||
		!legacyTextBytesBetween(meta.ThreadTitle, 0, 16_384) ||
		!legacyTextBytesBetween(meta.ThreadSourceQuery, 0, 1_048_576) ||
		len(meta.ThreadContext) > 16_000_000 {
		return SessionMeta{}, errors.New("legacy session thread projection is out of bounds")
	}
	for key, value := range meta.ThreadContext {
		if !legacyTextBytesBetween(key, 1, 256) || !legacyTextBytesBetween(value, 0, 16_384) {
			return SessionMeta{}, errors.New("legacy session thread context is out of bounds")
		}
	}
	return meta, nil
}

func decodeLegacyHistoryForImport(
	input sqlitestore.LegacyInput,
	skip int,
	audit *legacySessionImportAudit,
) ([]providers.Message, int, error) {
	if len(input.Data) == 0 {
		return []providers.Message{}, 0, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(input.Data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	lineNumber := 0
	history := make([]providers.Message, 0)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lineNumber++
		var message providers.Message
		if err := decodeSingleLegacyJSON(line, &message); err != nil {
			audit.skipped(input, "invalid-message-json", line, 1)
			continue
		}
		if messageutil.IsTransientAssistantThoughtMessage(message) {
			audit.skipped(input, "transient-message", line, 1)
			continue
		}
		if err := validateLegacyMessage(message); err != nil {
			audit.skipped(input, "invalid-message-record", line, 1)
			continue
		}
		if lineNumber <= skip {
			audit.skipped(input, "truncated-message", line, 1)
			continue
		}
		history = append(history, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	filtered := messageutil.FilterInvalidHistoryMessages(history)
	if dropped := len(history) - len(filtered); dropped > 0 {
		audit.skipped(input, "invalid-message-sequence", input.Data, dropped)
	}
	return filtered, len(filtered), nil
}

func filterAggregateLegacyMessages(
	messages []providers.Message,
	source sqlitestore.LegacyInput,
	audit *legacySessionImportAudit,
) []providers.Message {
	valid := make([]providers.Message, 0, len(messages))
	for _, message := range messages {
		encoded, _ := json.Marshal(message)
		switch {
		case messageutil.IsTransientAssistantThoughtMessage(message):
			audit.skipped(source, "transient-message", encoded, 1)
		case validateLegacyMessage(message) != nil:
			audit.skipped(source, "invalid-message-record", encoded, 1)
		default:
			valid = append(valid, message)
		}
	}
	filtered := messageutil.FilterInvalidHistoryMessages(valid)
	if dropped := len(valid) - len(filtered); dropped > 0 {
		audit.skipped(source, "invalid-message-sequence", source.Data, dropped)
	}
	return filtered
}

func validateLegacyMessage(message providers.Message) error {
	if !legacyTextBytesBetween(message.Role, 0, 1_024) ||
		!legacyTextBytesBetween(message.Content, 0, 10_485_759) ||
		!legacyTextBytesBetween(message.ModelName, 0, 4_096) ||
		!legacyTextBytesBetween(message.ReasoningContent, 0, 10_485_759) ||
		!legacyTextBytesBetween(message.ToolCallID, 0, 4_096) {
		return errors.New("legacy session message is out of bounds")
	}
	if message.CreatedAt != nil && !validLegacyTime(*message.CreatedAt) {
		return errors.New("legacy session message timestamp is invalid")
	}
	_, err := encodeStoredMessage(message)
	return err
}

func validateLegacyThreadMeta(thread *legacyThreadMeta) error {
	thread.ID = strings.TrimSpace(thread.ID)
	thread.PrimarySessionKey = strings.TrimSpace(thread.PrimarySessionKey)
	thread.Type = normalizeLegacyThreadType(thread.Type)
	thread.Registration = normalizeLegacyThreadRegistration(thread.Registration)
	thread.SessionKeys = uniqueLegacyStrings(append(
		[]string{thread.PrimarySessionKey}, thread.SessionKeys...,
	))
	thread.Aliases = uniqueLegacyStrings(thread.Aliases)
	thread.Type = normalizeLegacyThreadType(thread.Type)
	thread.Registration = normalizeLegacyThreadRegistration(thread.Registration)
	if !legacyTextBytesBetween(thread.ID, 1, 16_384) ||
		!legacyTextBytesBetween(thread.UISessionID, 0, 16_384) ||
		!legacyTextBytesBetween(thread.PrimarySessionKey, 1, 16_384) ||
		!legacyTextBytesBetween(thread.AgentID, 0, 16_384) ||
		!legacyTextBytesBetween(thread.OwnerIdentity, 0, 16_384) ||
		!legacyTextBytesBetween(thread.Title, 0, 16_384) ||
		!legacyTextBytesBetween(thread.Type, 0, 256) ||
		!legacyTextBytesBetween(thread.SourceQuery, 0, 1_048_576) ||
		!legacyTextBytesBetween(thread.Registration, 0, 256) ||
		!validLegacyTime(thread.CreatedAt) || !validLegacyTime(thread.UpdatedAt) ||
		(thread.DroppedAt != nil && !validLegacyTime(*thread.DroppedAt)) ||
		len(thread.Context) > 16_000_000 || len(thread.SessionKeys) > 16_000_000 ||
		len(thread.Aliases) > 16_000_000 {
		return errors.New("legacy thread is out of bounds")
	}
	for key, value := range thread.Context {
		if !legacyTextBytesBetween(key, 1, 256) || !legacyTextBytesBetween(value, 0, 16_384) {
			return errors.New("legacy thread context is out of bounds")
		}
	}
	for _, value := range append(append([]string(nil), thread.SessionKeys...), thread.Aliases...) {
		if !legacyTextBytesBetween(value, 1, 16_384) {
			return errors.New("legacy thread relationship is out of bounds")
		}
	}
	return nil
}

func normalizeLegacyThreadType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "coding", "code", "implementation", "implementing":
		return "coding"
	case "reviewing", "review", "pr", "pull_request":
		return "reviewing"
	case "investigating", "investigate", "debugging", "debug":
		return "investigating"
	default:
		return "general"
	}
}

func normalizeLegacyThreadRegistration(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "tool", "manual", "migrated":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "migrated"
	}
}

func validateLegacyHandoff(handoff *legacyThreadHandoff) error {
	handoff.ID = strings.TrimSpace(handoff.ID)
	handoff.OriginSessionKey = strings.TrimSpace(handoff.OriginSessionKey)
	handoff.TargetThreadID = strings.TrimSpace(handoff.TargetThreadID)
	return firstNonNilLegacyValidation(
		validateLegacyText(handoff.ID, 1, 16_384),
		validateLegacyText(handoff.OriginSessionKey, 1, 16_384),
		validateLegacyText(handoff.OriginSessionID, 0, 16_384),
		validateLegacyText(handoff.TargetThreadID, 1, 16_384),
		validateLegacyText(handoff.TargetSessionID, 0, 16_384),
		validateLegacyText(handoff.AgentID, 0, 16_384),
		validateLegacyText(handoff.Summary, 0, 1_048_576),
		validateLegacyTimestamp(handoff.CreatedAt),
	)
}

func validateLegacyText(value string, minimum, maximum int) error {
	if !legacyTextBytesBetween(value, minimum, maximum) {
		return errors.New("legacy text is out of bounds")
	}
	return nil
}

func validateLegacyTimestamp(value time.Time) error {
	if !validLegacyTime(value) {
		return errors.New("legacy timestamp is out of bounds")
	}
	return nil
}

func firstNonNilLegacyValidation(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func legacyTextBytesBetween(value string, minimum, maximum int) bool {
	length := len([]byte(value))
	return length >= minimum && length <= maximum
}

func validLegacyTime(value time.Time) bool {
	if value.IsZero() {
		return true
	}
	seconds := value.Unix()
	return seconds >= -62_167_219_200 && seconds <= 253_402_300_799 &&
		value.Nanosecond() >= 0 && value.Nanosecond() <= 999_999_999
}

func legacySessionKeyFromSanitizedBase(base string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(base)), "sk_v1_") {
		return base
	}
	return strings.ReplaceAll(base, "_", ":")
}

func insertLegacySession(
	ctx context.Context,
	conn *sql.Conn,
	key string,
	history []providers.Message,
	meta SessionMeta,
) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, nil
	}
	var exists int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE session_key = ?`, key).Scan(&exists); err != nil {
		return false, err
	}
	if exists != 0 {
		return false, nil
	}
	createdSeconds, createdNanos := sqliteTimeParts(meta.CreatedAt)
	updatedSeconds, updatedNanos := sqliteTimeParts(meta.UpdatedAt)
	if _, err := conn.ExecContext(ctx, `INSERT INTO sessions (
        session_key, summary, created_seconds, created_nanos, updated_seconds, updated_nanos, version
    ) VALUES (?, ?, ?, ?, ?, ?, 1)`, key, meta.Summary, createdSeconds, createdNanos,
		updatedSeconds, updatedNanos); err != nil {
		return false, err
	}
	meta.Aliases = normalizeAliases(key, meta.Aliases)
	if err := writeScopeConn(ctx, conn, key, meta.Scope); err != nil {
		return false, err
	}
	for index, alias := range meta.Aliases {
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_aliases (
            session_key, sequence, alias
        ) SELECT ?, ?, ? WHERE NOT EXISTS (
            SELECT 1 FROM sessions WHERE session_key = ?
        )`, key, index, alias, alias); err != nil {
			return false, err
		}
	}
	history = messageutil.FilterInvalidHistoryMessages(history)
	for index := range history {
		if history[index].CreatedAt == nil || history[index].CreatedAt.IsZero() {
			fallback := meta.UpdatedAt
			if fallback.IsZero() {
				fallback = meta.CreatedAt
			}
			if fallback.IsZero() {
				fallback = time.Unix(0, 0).UTC()
			}
			history[index].CreatedAt = &fallback
		}
		if err := insertMessageConn(ctx, conn, key, index, history[index]); err != nil {
			return false, err
		}
	}
	return true, nil
}

func insertLegacyThread(
	ctx context.Context,
	conn *sql.Conn,
	thread legacyThreadMeta,
) (bool, string, error) {
	thread.ID = strings.TrimSpace(thread.ID)
	thread.PrimarySessionKey = strings.TrimSpace(thread.PrimarySessionKey)
	if thread.ID == "" || thread.PrimarySessionKey == "" {
		return false, "invalid-thread-record", nil
	}
	var sessionExists int
	if err := conn.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sessions WHERE session_key = ?`,
		thread.PrimarySessionKey,
	).Scan(&sessionExists); err != nil {
		return false, "sqlite-error", err
	}
	if sessionExists == 0 {
		return false, "broken-thread-reference", nil
	}
	if thread.CreatedAt.IsZero() {
		thread.CreatedAt = time.Unix(0, 0).UTC()
	}
	if thread.UpdatedAt.IsZero() {
		thread.UpdatedAt = thread.CreatedAt
	}
	createdSeconds, createdNanos := sqliteRequiredTimeParts(thread.CreatedAt)
	updatedSeconds, updatedNanos := sqliteRequiredTimeParts(thread.UpdatedAt)
	droppedSeconds, droppedNanos := any(nil), any(nil)
	if thread.DroppedAt != nil {
		droppedSeconds, droppedNanos = sqliteTimeParts(*thread.DroppedAt)
	}
	result, err := conn.ExecContext(ctx, `INSERT INTO threads (
        thread_id, ui_session_id, primary_session_key, agent_id, owner_identity, title,
        thread_type, source_query, registration, dropped_seconds, dropped_nanos,
        created_seconds, created_nanos, updated_seconds, updated_nanos, version
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
    ON CONFLICT(thread_id) DO NOTHING`, thread.ID, firstLegacyValue(thread.UISessionID, thread.ID),
		thread.PrimarySessionKey, firstLegacyValue(thread.AgentID, "main"),
		firstLegacyValue(thread.OwnerIdentity, "unknown"), firstLegacyValue(thread.Title, "New thread"),
		firstLegacyValue(thread.Type, "general"), thread.SourceQuery,
		firstLegacyValue(thread.Registration, "migrated"), droppedSeconds, droppedNanos,
		createdSeconds, createdNanos, updatedSeconds, updatedNanos)
	if err != nil {
		return false, "sqlite-error", err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, "sqlite-error", err
	}
	if inserted == 0 {
		return false, "thread-identity-conflict", nil
	}
	contextKeys := make([]string, 0, len(thread.Context))
	for key := range thread.Context {
		contextKeys = append(contextKeys, key)
	}
	sort.Strings(contextKeys)
	for _, key := range contextKeys {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO thread_context(thread_id, key, value) VALUES (?, ?, ?)`,
			thread.ID, key, thread.Context[key]); err != nil {
			return false, "sqlite-error", err
		}
	}
	for index, alias := range uniqueLegacyStrings(thread.Aliases) {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO thread_aliases(thread_id, sequence, alias) VALUES (?, ?, ?)`,
			thread.ID, index, alias); err != nil {
			return false, "sqlite-error", err
		}
	}
	sessionKeys := uniqueLegacyStrings(append([]string{thread.PrimarySessionKey}, thread.SessionKeys...))
	position := 0
	for _, sessionKey := range sessionKeys {
		var exists int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sessions WHERE session_key = ?`, sessionKey).Scan(&exists); err != nil {
			return false, "sqlite-error", err
		}
		if exists == 0 {
			continue
		}
		isPrimary := 0
		if sessionKey == thread.PrimarySessionKey {
			isPrimary = 1
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO thread_sessions (
            thread_id, sequence, session_key, is_primary
        ) VALUES (?, ?, ?, ?)`, thread.ID, position, sessionKey, isPrimary); err != nil {
			return false, "sqlite-error", err
		}
		attachedAt, hasAttachedAt := thread.AttachedAt[sessionKey]
		if sessionKey == thread.PrimarySessionKey && (!hasAttachedAt || attachedAt.IsZero()) {
			attachedAt = thread.UpdatedAt
			hasAttachedAt = true
		}
		if hasAttachedAt {
			if attachedAt.IsZero() {
				attachedAt = time.Unix(0, 0).UTC()
			}
			seconds, nanos := sqliteRequiredTimeParts(attachedAt)
			if _, err := conn.ExecContext(ctx, `INSERT INTO session_thread_links (
                    session_key, thread_id, attached_seconds, attached_nanos
                ) VALUES (?, ?, ?, ?) ON CONFLICT(session_key) DO NOTHING`,
				sessionKey, thread.ID, seconds, nanos); err != nil {
				return false, "sqlite-error", err
			}
		}
		position++
	}
	return true, "", nil
}

func insertLegacyHandoff(
	ctx context.Context,
	conn *sql.Conn,
	handoff legacyThreadHandoff,
) (bool, string, error) {
	var originExists, threadExists int
	if err := conn.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sessions WHERE session_key = ?`,
		handoff.OriginSessionKey,
	).Scan(&originExists); err != nil {
		return false, "sqlite-error", err
	}
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM threads WHERE thread_id = ?`, handoff.TargetThreadID).Scan(&threadExists); err != nil {
		return false, "sqlite-error", err
	}
	if originExists == 0 || threadExists == 0 {
		return false, "broken-handoff-reference", nil
	}
	if handoff.CreatedAt.IsZero() {
		handoff.CreatedAt = time.Unix(0, 0).UTC()
	}
	seconds, nanos := sqliteRequiredTimeParts(handoff.CreatedAt)
	result, err := conn.ExecContext(ctx, `INSERT INTO thread_handoffs (
        handoff_id, origin_session_key, origin_session_id, target_thread_id,
        target_session_id, agent_id, summary, created_seconds, created_nanos, version
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1) ON CONFLICT(handoff_id) DO NOTHING`,
		handoff.ID, handoff.OriginSessionKey, handoff.OriginSessionID, handoff.TargetThreadID,
		handoff.TargetSessionID, handoff.AgentID, handoff.Summary, seconds, nanos)
	if err != nil {
		return false, "sqlite-error", err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, "sqlite-error", err
	}
	if inserted == 0 {
		return false, "handoff-identity-conflict", nil
	}
	return true, "", nil
}

func firstLegacyValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func uniqueLegacyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
