package threads

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

const (
	RegistrationAuto     = "auto"
	RegistrationTool     = "tool"
	RegistrationManual   = "manual"
	RegistrationMigrated = "migrated"
)

func (s Store) withDefaults() Store {
	if strings.TrimSpace(s.Workspace) == "" {
		if strings.TrimSpace(s.Dir) != "" {
			s.Workspace = filepath.Dir(s.Dir)
		} else {
			s.Workspace = ResolveWorkspace("")
		}
	}
	if strings.TrimSpace(s.Dir) == "" {
		s.Dir = filepath.Join(s.Workspace, "sessions")
	}
	if strings.TrimSpace(s.ThreadsDir) == "" {
		s.ThreadsDir = filepath.Join(s.Workspace, "threads")
	}
	if strings.TrimSpace(s.HandoffsDir) == "" {
		s.HandoffsDir = filepath.Join(s.ThreadsDir, "handoffs")
	}
	return s
}

func (s Store) ensureThreadDirs() error {
	s = s.withDefaults()
	if err := os.MkdirAll(s.ThreadsDir, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(s.HandoffsDir, 0o755)
}

func (s Store) threadPath(id string) string {
	s = s.withDefaults()
	return filepath.Join(s.ThreadsDir, sanitizeThreadID(id)+".json")
}

func (s Store) handoffPath(id string) string {
	s = s.withDefaults()
	return filepath.Join(s.HandoffsDir, sanitizeThreadID(id)+".json")
}

func (s Store) sessionMetaPath(sessionKey string) string {
	s = s.withDefaults()
	return filepath.Join(s.Dir, sanitizeSessionKey(sessionKey)+".meta.json")
}

func (s Store) sessionJSONLPath(sessionKey string) string {
	s = s.withDefaults()
	return filepath.Join(s.Dir, sanitizeSessionKey(sessionKey)+".jsonl")
}

func (s Store) readThreadMeta(id string) (ThreadMeta, error) {
	data, err := os.ReadFile(s.threadPath(id))
	if err != nil {
		return ThreadMeta{}, err
	}
	var meta ThreadMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return ThreadMeta{}, err
	}
	meta.ID = strings.TrimSpace(meta.ID)
	if meta.ID == "" {
		meta.ID = strings.TrimSpace(id)
	}
	return normalizeThreadMeta(meta), nil
}

func (s Store) GetMeta(id string) (ThreadMeta, bool, error) {
	meta, err := s.readThreadMeta(id)
	if os.IsNotExist(err) {
		return ThreadMeta{}, false, nil
	}
	if err != nil {
		return ThreadMeta{}, false, err
	}
	return meta, true, nil
}

func (s Store) writeThreadMeta(meta ThreadMeta) error {
	s = s.withDefaults()
	if err := s.ensureThreadDirs(); err != nil {
		return err
	}
	meta = normalizeThreadMeta(meta)
	if meta.ID == "" {
		return errors.New("threads: thread id is empty")
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(s.threadPath(meta.ID), data, 0o644)
}

func (s Store) listThreadMetas() ([]ThreadMeta, error) {
	s = s.withDefaults()
	if err := s.migrateSessionThreads(context.Background()); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.ThreadsDir)
	if os.IsNotExist(err) {
		return []ThreadMeta{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]ThreadMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		meta, err := s.readThreadMeta(id)
		if err != nil {
			continue
		}
		items = append(items, meta)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func (s Store) threadFromRegistryMeta(meta ThreadMeta) (Thread, bool) {
	s = s.withDefaults()
	meta = normalizeThreadMeta(meta)
	if meta.ID == "" || meta.PrimarySessionKey == "" {
		return Thread{}, false
	}

	sessionMeta, _ := readMeta(s.sessionMetaPath(meta.PrimarySessionKey), meta.PrimarySessionKey)
	messages, _ := readMessages(s.sessionJSONLPath(meta.PrimarySessionKey), sessionMeta.Skip)
	visible := visibleMessages(messages)
	preview := ""
	for _, msg := range visible {
		if msg.Role == "user" {
			preview = messagePreview(msg)
			break
		}
	}
	if preview == "" {
		preview = strings.TrimSpace(sessionMeta.Summary)
	}
	if preview == "" {
		preview = strings.TrimSpace(meta.SourceQuery)
	}
	if preview == "" {
		preview = strings.TrimSpace(meta.Title)
	}
	if preview == "" {
		preview = "(empty)"
	}

	title := strings.TrimSpace(meta.Title)
	if title == "" {
		title = preview
	}
	if title == "" {
		title = "New thread"
	}
	updated := meta.UpdatedAt
	if sessionMeta.UpdatedAt.After(updated) {
		updated = sessionMeta.UpdatedAt
	}
	if updated.IsZero() {
		updated = meta.CreatedAt
	}
	if updated.IsZero() {
		if info, err := os.Stat(s.sessionJSONLPath(meta.PrimarySessionKey)); err == nil {
			updated = info.ModTime()
		}
	}
	created := meta.CreatedAt
	if created.IsZero() {
		created = sessionMeta.CreatedAt
	}
	if created.IsZero() {
		created = updated
	}
	if created.IsZero() && updated.IsZero() {
		return Thread{}, false
	}

	return Thread{
		ID:                meta.ID,
		UISessionID:       meta.UISessionID,
		SessionKey:        meta.PrimarySessionKey,
		PrimarySessionKey: meta.PrimarySessionKey,
		AgentID:           meta.AgentID,
		OwnerIdentity:     meta.OwnerIdentity,
		Title:             truncateRunes(title, 80),
		Preview:           truncateRunes(preview, 120),
		Type:              NormalizeType(meta.Type),
		Context:           MergeContext(scopeContext(sessionMeta.Scope), meta.Context),
		MessageCount:      len(visible),
		Created:           created,
		Updated:           updated,
		SourceQuery:       strings.TrimSpace(meta.SourceQuery),
		Discoverable:      meta.DroppedAt == nil,
		DroppedAt:         meta.DroppedAt,
	}, true
}

func (s Store) CreateThread(ctx context.Context, req CreateRequest) (Thread, error) {
	s = s.withDefaults()
	now := time.Now()
	threadID := strings.TrimSpace(req.ID)
	if threadID == "" {
		threadID = GenerateSessionID()
	}
	primarySessionKey := strings.TrimSpace(req.PrimarySessionKey)
	if primarySessionKey == "" {
		return Thread{}, errors.New("threads: primary session key is empty")
	}
	uiSessionID := strings.TrimSpace(req.UISessionID)
	if uiSessionID == "" {
		uiSessionID = threadID
	}
	registration := normalizeRegistration(req.Registration)
	if registration == "" {
		registration = RegistrationManual
	}
	sourceQuery := strings.TrimSpace(firstNonEmpty(req.SourceQuery, req.Title, "New thread"))
	sessionKeys := append([]string{primarySessionKey}, req.SessionKeys...)
	meta := ThreadMeta{
		ID:                threadID,
		UISessionID:       uiSessionID,
		PrimarySessionKey: primarySessionKey,
		AgentID:           firstNonEmpty(req.AgentID, routingAgentFromSessionKey(primarySessionKey), "main"),
		OwnerIdentity:     firstNonEmpty(req.OwnerIdentity, "unknown"),
		Title:             truncateRunes(firstNonEmpty(req.Title, sourceQuery, "New thread"), 80),
		Type:              NormalizeType(firstNonEmpty(req.Type, InferType(req.Title+" "+sourceQuery))),
		Context:           MergeContext(ExtractContext(sourceQuery+" "+req.Title), req.Context),
		SourceQuery:       sourceQuery,
		SessionKeys:       uniqueStrings(sessionKeys),
		Registration:      registration,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.writeThreadMeta(meta); err != nil {
		return Thread{}, err
	}
	if err := s.setSessionThreadLink(primarySessionKey, threadID, now); err != nil {
		return Thread{}, err
	}
	_ = ctx
	thread, ok := s.threadFromRegistryMeta(meta)
	if !ok {
		return Thread{}, errors.New("threads: created thread could not be loaded")
	}
	return thread, nil
}

func (s Store) UpdateThread(id string, req UpdateRequest) (Thread, bool, error) {
	meta, err := s.readThreadMeta(id)
	if os.IsNotExist(err) {
		return Thread{}, false, nil
	}
	if err != nil {
		return Thread{}, false, err
	}
	if strings.TrimSpace(req.Title) != "" {
		meta.Title = truncateRunes(req.Title, 80)
	}
	if strings.TrimSpace(req.Type) != "" {
		meta.Type = NormalizeType(req.Type)
	}
	if req.Context != nil {
		meta.Context = cleanContext(req.Context)
	}
	if strings.TrimSpace(req.SourceQuery) != "" {
		meta.SourceQuery = strings.TrimSpace(req.SourceQuery)
	}
	if req.Discoverable != nil {
		if *req.Discoverable {
			meta.DroppedAt = nil
		} else if meta.DroppedAt == nil {
			now := time.Now()
			meta.DroppedAt = &now
		}
	}
	meta.UpdatedAt = time.Now()
	if err := s.writeThreadMeta(meta); err != nil {
		return Thread{}, false, err
	}
	thread, ok := s.threadFromRegistryMeta(meta)
	return thread, ok, nil
}

func (s Store) DropThread(id string) (Thread, bool, error) {
	thread, ok, err := s.Get(id)
	if err != nil || !ok {
		return Thread{}, ok, err
	}
	discoverable := false
	return s.UpdateThread(thread.ID, UpdateRequest{Discoverable: &discoverable})
}

func (s Store) RegisterCurrent(ctx context.Context, cfg CreateRequest, scope *session.SessionScope) (Thread, error) {
	s = s.withDefaults()
	sessionKey := strings.TrimSpace(cfg.PrimarySessionKey)
	if sessionKey == "" {
		return Thread{}, errors.New("threads: current session key is empty")
	}
	uiSessionID := strings.TrimSpace(cfg.UISessionID)
	if uiSessionID == "" {
		if scope != nil {
			if id, ok := picoSessionIDFromScope(*scope); ok {
				uiSessionID = id
			}
		}
	}
	if uiSessionID == "" {
		uiSessionID = strings.TrimSpace(cfg.ID)
	}
	if uiSessionID == "" {
		uiSessionID = sessionKey
	}
	cfg.UISessionID = uiSessionID
	cfg.Registration = firstNonEmpty(cfg.Registration, RegistrationTool)
	cfg.OwnerIdentity = firstNonEmpty(cfg.OwnerIdentity, ownerIdentityFromScope(scope))
	return s.CreateThread(ctx, cfg)
}

func (s Store) AttachCurrent(ctx context.Context, req AttachRequest) (Thread, ThreadHandoff, error) {
	s = s.withDefaults()
	if strings.TrimSpace(req.ThreadID) == "" {
		return Thread{}, ThreadHandoff{}, errors.New("threads: thread id is empty")
	}
	if strings.TrimSpace(req.SessionKey) == "" {
		return Thread{}, ThreadHandoff{}, errors.New("threads: current session key is empty")
	}
	meta, err := s.readThreadMeta(req.ThreadID)
	if err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	now := time.Now()
	meta.SessionKeys = uniqueStrings(append(meta.SessionKeys, req.SessionKey))
	if req.OwnerIdentity != "" && meta.OwnerIdentity == "" {
		meta.OwnerIdentity = req.OwnerIdentity
	}
	if req.AgentID != "" && meta.AgentID == "" {
		meta.AgentID = req.AgentID
	}
	meta.UpdatedAt = now
	if err := s.writeThreadMeta(meta); err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	if err := s.setSessionThreadLink(req.SessionKey, meta.ID, now); err != nil {
		return Thread{}, ThreadHandoff{}, err
	}

	handoff := ThreadHandoff{
		ID:               GenerateHandoffID(),
		OriginSessionKey: req.SessionKey,
		OriginSessionID:  strings.TrimSpace(req.OriginSessionID),
		TargetThreadID:   meta.ID,
		TargetSessionID:  meta.UISessionID,
		AgentID:          firstNonEmpty(req.AgentID, meta.AgentID),
		Summary:          strings.TrimSpace(req.Summary),
		CreatedAt:        now,
	}
	if err := s.writeHandoff(handoff); err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	if handoff.Summary != "" && meta.PrimarySessionKey != req.SessionKey {
		if store, err := memory.NewJSONLStore(s.Dir); err == nil {
			_ = store.AddFullMessage(ctx, meta.PrimarySessionKey, providers.Message{
				Role:    "user",
				Content: "Continued from another session.\n\n" + handoff.Summary,
			})
		}
	}
	thread, ok := s.threadFromRegistryMeta(meta)
	if !ok {
		return Thread{}, ThreadHandoff{}, errors.New("threads: attached thread could not be loaded")
	}
	return thread, handoff, nil
}

func (s Store) DetachCurrent(sessionKey string) error {
	return s.clearSessionThreadLink(sessionKey)
}

func (s Store) ReturnToOrigin(handoffID string) (ThreadHandoff, bool, error) {
	handoff, err := s.readHandoff(handoffID)
	if os.IsNotExist(err) {
		return ThreadHandoff{}, false, nil
	}
	if err != nil {
		return ThreadHandoff{}, false, err
	}
	return handoff, true, nil
}

func (s Store) writeHandoff(handoff ThreadHandoff) error {
	s = s.withDefaults()
	if err := s.ensureThreadDirs(); err != nil {
		return err
	}
	if strings.TrimSpace(handoff.ID) == "" {
		return errors.New("threads: handoff id is empty")
	}
	data, err := json.MarshalIndent(handoff, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(s.handoffPath(handoff.ID), data, 0o644)
}

func (s Store) readHandoff(id string) (ThreadHandoff, error) {
	data, err := os.ReadFile(s.handoffPath(id))
	if err != nil {
		return ThreadHandoff{}, err
	}
	var handoff ThreadHandoff
	if err := json.Unmarshal(data, &handoff); err != nil {
		return ThreadHandoff{}, err
	}
	return handoff, nil
}

func (s Store) setSessionThreadLink(sessionKey, threadID string, attachedAt time.Time) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	meta, err := readMeta(s.sessionMetaPath(sessionKey), sessionKey)
	if err != nil {
		return err
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = attachedAt
	}
	meta.UpdatedAt = attachedAt
	meta.Key = sessionKey
	meta.ThreadID = strings.TrimSpace(threadID)
	meta.ThreadAttachedAt = attachedAt
	return writeMeta(s.sessionMetaPath(sessionKey), meta)
}

func (s Store) clearSessionThreadLink(sessionKey string) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	meta, err := readMeta(s.sessionMetaPath(sessionKey), sessionKey)
	if err != nil {
		return err
	}
	meta.ThreadID = ""
	meta.ThreadAttachedAt = time.Time{}
	meta.UpdatedAt = time.Now()
	return writeMeta(s.sessionMetaPath(sessionKey), meta)
}

func (s Store) migrateSessionThreads(ctx context.Context) error {
	s = s.withDefaults()
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".meta.json")
		meta, err := readMeta(filepath.Join(s.Dir, entry.Name()), sessionKeyFromSanitizedBase(base))
		if err != nil {
			continue
		}
		if !shouldMigrateSessionMeta(meta) {
			continue
		}
		threadID := strings.TrimSpace(meta.ThreadID)
		picoID, hasPicoID := picoSessionIDFromMeta(meta)
		if threadID == "" && hasPicoID {
			threadID = picoID
		}
		if threadID == "" {
			threadID = GenerateSessionID()
		}
		if _, err := s.readThreadMeta(threadID); err == nil {
			continue
		}
		scope := scopeFromMeta(meta)
		title, _ := titleAndPreview(meta, visibleMessages(mustReadMessages(s.sessionJSONLPath(meta.Key), meta.Skip)))
		reg := RegistrationMigrated
		if meta.ThreadID != "" {
			reg = RegistrationTool
		}
		uiSessionID := threadID
		if hasPicoID {
			uiSessionID = picoID
		}
		threadMeta := ThreadMeta{
			ID:                threadID,
			UISessionID:       uiSessionID,
			PrimarySessionKey: meta.Key,
			AgentID:           agentIDFromScope(scope),
			OwnerIdentity:     ownerIdentityFromScope(scope),
			Title:             title,
			Type:              NormalizeType(firstNonEmpty(meta.ThreadType, InferType(title+" "+meta.Summary))),
			Context:           MergeContext(scopeContext(meta.Scope), meta.ThreadContext),
			SourceQuery:       strings.TrimSpace(meta.ThreadSourceQuery),
			SessionKeys:       []string{meta.Key},
			Aliases:           append([]string(nil), meta.Aliases...),
			Registration:      reg,
			CreatedAt:         meta.CreatedAt,
			UpdatedAt:         meta.UpdatedAt,
		}
		if threadMeta.CreatedAt.IsZero() || threadMeta.UpdatedAt.IsZero() {
			if info, err := os.Stat(s.sessionJSONLPath(meta.Key)); err == nil {
				if threadMeta.CreatedAt.IsZero() {
					threadMeta.CreatedAt = info.ModTime()
				}
				if threadMeta.UpdatedAt.IsZero() {
					threadMeta.UpdatedAt = info.ModTime()
				}
			}
		}
		if err := s.writeThreadMeta(threadMeta); err != nil {
			return err
		}
		if meta.ThreadID == "" {
			_ = s.setSessionThreadLink(meta.Key, threadID, time.Now())
		}
	}
	_ = ctx
	return nil
}

func shouldMigrateSessionMeta(meta memory.SessionMeta) bool {
	if strings.TrimSpace(meta.Key) == "" {
		return false
	}
	if strings.TrimSpace(meta.ThreadID) != "" ||
		strings.TrimSpace(meta.ThreadTitle) != "" ||
		strings.TrimSpace(meta.ThreadType) != "" ||
		strings.TrimSpace(meta.ThreadSourceQuery) != "" ||
		len(meta.ThreadContext) > 0 {
		return true
	}
	_, ok := picoSessionIDFromMeta(meta)
	return ok
}

func normalizeThreadMeta(meta ThreadMeta) ThreadMeta {
	meta.ID = strings.TrimSpace(meta.ID)
	meta.UISessionID = strings.TrimSpace(meta.UISessionID)
	if meta.UISessionID == "" {
		meta.UISessionID = meta.ID
	}
	meta.PrimarySessionKey = strings.TrimSpace(meta.PrimarySessionKey)
	meta.AgentID = strings.TrimSpace(meta.AgentID)
	if meta.AgentID == "" {
		meta.AgentID = "main"
	}
	meta.OwnerIdentity = strings.TrimSpace(meta.OwnerIdentity)
	if meta.OwnerIdentity == "" {
		meta.OwnerIdentity = "unknown"
	}
	meta.Title = truncateRunes(firstNonEmpty(meta.Title, meta.SourceQuery, "New thread"), 80)
	meta.Type = NormalizeType(meta.Type)
	meta.Context = cleanContext(meta.Context)
	meta.SourceQuery = strings.TrimSpace(meta.SourceQuery)
	meta.SessionKeys = uniqueStrings(append([]string{meta.PrimarySessionKey}, meta.SessionKeys...))
	meta.Aliases = uniqueStrings(meta.Aliases)
	meta.Registration = normalizeRegistration(meta.Registration)
	if meta.Registration == "" {
		meta.Registration = RegistrationManual
	}
	if meta.DroppedAt != nil && meta.DroppedAt.IsZero() {
		meta.DroppedAt = nil
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now()
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = meta.CreatedAt
	}
	return meta
}

func normalizeRegistration(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RegistrationAuto:
		return RegistrationAuto
	case RegistrationTool:
		return RegistrationTool
	case RegistrationManual:
		return RegistrationManual
	case RegistrationMigrated:
		return RegistrationMigrated
	default:
		return ""
	}
}

func GenerateHandoffID() string {
	return "handoff-" + GenerateSessionID()
}

func sanitizeThreadID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "thread"
	}
	return sanitizeSessionKey(id)
}

func scopeFromMeta(meta memory.SessionMeta) *session.SessionScope {
	if len(meta.Scope) == 0 {
		return nil
	}
	var scope session.SessionScope
	if err := json.Unmarshal(meta.Scope, &scope); err != nil {
		return nil
	}
	return &scope
}

func ownerIdentityFromScope(scope *session.SessionScope) string {
	if scope == nil {
		return "unknown"
	}
	for _, key := range []string{"sender", "chat", "space"} {
		if value := strings.TrimSpace(scope.Values[key]); value != "" {
			return strings.ToLower(value)
		}
	}
	if scope.Account != "" {
		return strings.ToLower(strings.TrimSpace(scope.Account))
	}
	if scope.AgentID != "" {
		return "agent:" + strings.ToLower(strings.TrimSpace(scope.AgentID))
	}
	return "unknown"
}

func agentIDFromScope(scope *session.SessionScope) string {
	if scope == nil || strings.TrimSpace(scope.AgentID) == "" {
		return "main"
	}
	return strings.TrimSpace(scope.AgentID)
}

func routingAgentFromSessionKey(sessionKey string) string {
	if parsed := session.ParseLegacyAgentSessionKey(sessionKey); parsed != nil {
		return parsed.AgentID
	}
	return ""
}

func mustReadMessages(path string, skip int) []providers.Message {
	messages, err := readMessages(path, skip)
	if err != nil {
		return nil
	}
	return messages
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
