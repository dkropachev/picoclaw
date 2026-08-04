package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
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
	meta, data, err := marshalThreadMeta(meta)
	if err != nil {
		return err
	}
	if s.testHooks != nil && s.testHooks.writeThreadMeta != nil {
		if err := s.testHooks.writeThreadMeta(meta); err != nil {
			return err
		}
	}
	return fileutil.WriteFileAtomic(s.threadPath(meta.ID), data, 0o644)
}

func marshalThreadMeta(meta ThreadMeta) (ThreadMeta, []byte, error) {
	meta = normalizeThreadMeta(meta)
	if meta.ID == "" {
		return ThreadMeta{}, nil, errors.New("threads: thread id is empty")
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return ThreadMeta{}, nil, err
	}
	return meta, data, nil
}

type durableFileBackup struct {
	path     string
	data     []byte
	mode     os.FileMode
	existed  bool
	expected []byte
}

func readDurableFileBackup(path string) (durableFileBackup, error) {
	backup := durableFileBackup{path: path, mode: 0o644}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return backup, nil
	}
	if err != nil {
		return durableFileBackup{}, err
	}
	backup.data = data
	backup.existed = true
	if info, statErr := os.Stat(path); statErr == nil {
		backup.mode = info.Mode().Perm()
	} else {
		return durableFileBackup{}, statErr
	}
	return backup, nil
}

func (b durableFileBackup) expecting(data []byte) durableFileBackup {
	b.expected = append([]byte(nil), data...)
	return b
}

func (b durableFileBackup) restore() error {
	current, err := os.ReadFile(b.path)
	currentExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if currentExists == b.existed && (!currentExists || slices.Equal(current, b.data)) {
		return nil
	}
	if !currentExists || b.expected == nil || !slices.Equal(current, b.expected) {
		return errors.New("threads: artifact changed concurrently; conditional rollback refused")
	}
	if b.existed {
		return fileutil.WriteFileAtomic(b.path, b.data, b.mode)
	}
	if err := fileutil.RemoveDurable(b.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
	state, err := s.readOrdinarySessionState(
		context.Background(),
		meta.PrimarySessionKey,
		true,
	)
	if err != nil {
		// Registry records are derived. Ownership, metadata, and scope failures
		// hide the complete projection rather than exposing stale identifiers.
		return Thread{}, false
	}
	return threadFromOrdinarySessionState(meta, state)
}

func threadFromOrdinarySessionState(
	meta ThreadMeta,
	state ordinarySessionState,
) (Thread, bool) {
	meta = normalizeThreadMeta(meta)
	if !state.found || state.key == "" {
		return Thread{}, false
	}
	if state.key != meta.PrimarySessionKey {
		meta.SessionKeys = uniqueStrings(append([]string{state.key}, meta.SessionKeys...))
		meta.PrimarySessionKey = state.key
	}
	sessionMeta := state.meta
	messages := state.history
	historyModifiedAt := state.historyModifiedAt
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
		updated = historyModifiedAt
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
	now := time.Now().UTC()
	threadID := strings.TrimSpace(req.ID)
	if threadID == "" {
		threadID = GenerateSessionID()
	}
	primarySessionKey := strings.TrimSpace(req.PrimarySessionKey)
	if primarySessionKey == "" {
		return Thread{}, errors.New("threads: primary session key is empty")
	}
	primarySessionKey, err := s.canonicalSessionKey(ctx, primarySessionKey)
	if err != nil {
		return Thread{}, err
	}
	if s.testHooks != nil && s.testHooks.afterCreatePreflight != nil {
		s.testHooks.afterCreatePreflight()
	}
	registryBackup, err := readDurableFileBackup(s.threadPath(threadID))
	if err != nil {
		return Thread{}, fmt.Errorf("threads: snapshot registry before create: %w", err)
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
	// Claim the session under the JSONL store's directory/session locks before
	// publishing a registry record. If review admission won after the preflight,
	// the callback rejects it without leaving a discoverable thread behind. If
	// this callback wins, the resulting metadata makes later review admission
	// conflict before the registry record is published.
	linkChange, err := s.claimSessionThreadLink(ctx, primarySessionKey, threadID, now, false)
	if err != nil {
		return Thread{}, err
	}
	primarySessionKey = linkChange.key
	meta.PrimarySessionKey = primarySessionKey
	meta.SessionKeys = uniqueStrings(append([]string{primarySessionKey}, meta.SessionKeys...))
	_, registryExpected, err := marshalThreadMeta(meta)
	if err != nil {
		return Thread{}, rollbackThreadOperation(ctx, err, nil, linkChange)
	}
	registryBackup = registryBackup.expecting(registryExpected)
	if err := s.writeThreadMeta(meta); err != nil {
		return Thread{}, rollbackThreadOperation(ctx, err, []durableFileBackup{registryBackup}, linkChange)
	}
	thread, ok := s.threadFromRegistryMeta(meta)
	if !ok {
		err := errors.New("threads: created thread could not be loaded")
		return Thread{}, rollbackThreadOperation(ctx, err, []durableFileBackup{registryBackup}, linkChange)
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
	state, err := s.readOrdinarySessionState(
		context.Background(),
		meta.PrimarySessionKey,
		true,
	)
	if err != nil {
		return Thread{}, false, err
	}
	meta.PrimarySessionKey = state.key
	meta.SessionKeys = uniqueStrings(append([]string{state.key}, meta.SessionKeys...))
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
			now := time.Now().UTC()
			meta.DroppedAt = &now
		}
	}
	meta.UpdatedAt = time.Now().UTC()
	thread, ok := threadFromOrdinarySessionState(meta, state)
	if !ok {
		return Thread{}, false, errors.New("threads: updated thread could not be projected")
	}
	if err := s.writeThreadMeta(meta); err != nil {
		return Thread{}, false, err
	}
	return thread, true, nil
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
	if err := rejectReviewThreadSessionScope(scope); err != nil {
		return Thread{}, err
	}
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
	if err := rejectReviewThreadSessionScope(req.Scope); err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
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
	originSessionKey, err := s.canonicalSessionKey(ctx, req.SessionKey)
	if err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	primarySessionKey, err := s.canonicalSessionKey(ctx, meta.PrimarySessionKey)
	if err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	if s.testHooks != nil && s.testHooks.afterAttachPreflight != nil {
		s.testHooks.afterAttachPreflight()
	}
	// Registry targets must already be authoritative ordinary sessions. This
	// read is repeated after the deterministic race boundary: a missing target
	// is never synthesized and no origin/registry/handoff state changes first.
	targetState, err := s.readOrdinarySessionState(ctx, primarySessionKey, true)
	if err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	primarySessionKey = targetState.key
	now := time.Now().UTC()
	meta.PrimarySessionKey = targetState.key
	meta.SessionKeys = uniqueStrings(append(meta.SessionKeys, originSessionKey, targetState.key))
	if req.OwnerIdentity != "" && meta.OwnerIdentity == "" {
		meta.OwnerIdentity = req.OwnerIdentity
	}
	if req.AgentID != "" && meta.AgentID == "" {
		meta.AgentID = req.AgentID
	}
	meta.UpdatedAt = now
	handoff := ThreadHandoff{
		ID:               GenerateHandoffID(),
		OriginSessionKey: originSessionKey,
		OriginSessionID:  strings.TrimSpace(req.OriginSessionID),
		TargetThreadID:   meta.ID,
		TargetSessionID:  meta.UISessionID,
		AgentID:          firstNonEmpty(req.AgentID, meta.AgentID),
		Summary:          strings.TrimSpace(req.Summary),
		CreatedAt:        now,
	}
	projected, ok := threadFromOrdinarySessionState(meta, targetState)
	if !ok {
		return Thread{}, ThreadHandoff{}, errors.New("threads: attached thread could not be projected")
	}
	registryBackup, err := readDurableFileBackup(s.threadPath(meta.ID))
	if err != nil {
		return Thread{}, ThreadHandoff{}, fmt.Errorf("threads: snapshot registry before attach: %w", err)
	}
	handoffBackup, err := readDurableFileBackup(s.handoffPath(handoff.ID))
	if err != nil {
		return Thread{}, ThreadHandoff{}, fmt.Errorf("threads: snapshot handoff before attach: %w", err)
	}

	// Origin ownership is the commit guard. The strict mutation arbitrates with
	// review admission before registry, handoff, or target history changes.
	linkChange, err := s.claimSessionThreadLink(ctx, originSessionKey, meta.ID, now, false)
	if err != nil {
		return Thread{}, ThreadHandoff{}, err
	}
	originSessionKey = linkChange.key
	meta.SessionKeys = uniqueStrings(append(meta.SessionKeys, originSessionKey))
	handoff.OriginSessionKey = originSessionKey
	_, registryExpected, err := marshalThreadMeta(meta)
	if err != nil {
		return Thread{}, ThreadHandoff{}, rollbackThreadOperation(
			ctx,
			err,
			nil,
			linkChange,
		)
	}
	handoffExpected, err := marshalHandoff(handoff)
	if err != nil {
		return Thread{}, ThreadHandoff{}, rollbackThreadOperation(
			ctx,
			err,
			nil,
			linkChange,
		)
	}
	registryBackup = registryBackup.expecting(registryExpected)
	handoffBackup = handoffBackup.expecting(handoffExpected)
	if err := s.writeThreadMeta(meta); err != nil {
		return Thread{}, ThreadHandoff{}, rollbackThreadOperation(
			ctx,
			err,
			[]durableFileBackup{registryBackup},
			linkChange,
		)
	}
	if err := s.writeHandoff(handoff); err != nil {
		return Thread{}, ThreadHandoff{}, rollbackThreadOperation(
			ctx,
			err,
			[]durableFileBackup{registryBackup, handoffBackup},
			linkChange,
		)
	}
	if handoff.Summary != "" && primarySessionKey != originSessionKey {
		message := providers.Message{
			Role:    "user",
			Content: "Continued from another session.\n\n" + handoff.Summary,
		}
		var appendErr error
		if s.testHooks != nil && s.testHooks.appendSummary != nil {
			appendErr = s.testHooks.appendSummary(ctx, primarySessionKey, message)
		} else if store, openErr := memory.NewJSONLStore(s.Dir); openErr != nil {
			appendErr = openErr
		} else {
			appendErr = store.AddFullMessage(ctx, primarySessionKey, message)
		}
		if appendErr != nil {
			// Attach and handoff are already durable. Summary projection is an
			// explicitly best-effort enrichment; failure cannot safely roll back a
			// possibly committed append, so report it operationally without turning
			// a successful attach into a partial-state error.
			log.Printf("threads: attach %s summary projection failed: %v", handoff.ID, appendErr)
		}
	}
	return projected, handoff, nil
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
	// Handoffs are derived pointers. Validate both authoritative endpoints on
	// every return so stale files cannot redirect into, or reveal, a review or
	// corrupt session after their original creation.
	if _, stateErr := s.readOrdinarySessionState(
		context.Background(),
		handoff.OriginSessionKey,
		true,
	); stateErr != nil {
		if errors.Is(stateErr, errSessionMissing) {
			return ThreadHandoff{}, false, nil
		}
		return ThreadHandoff{}, false, stateErr
	}
	targetMeta, err := s.readThreadMeta(handoff.TargetThreadID)
	if os.IsNotExist(err) {
		return ThreadHandoff{}, false, nil
	}
	if err != nil {
		return ThreadHandoff{}, false, err
	}
	if _, err := s.readOrdinarySessionState(
		context.Background(),
		targetMeta.PrimarySessionKey,
		true,
	); err != nil {
		if errors.Is(err, errSessionMissing) {
			return ThreadHandoff{}, false, nil
		}
		return ThreadHandoff{}, false, err
	}
	return handoff, true, nil
}

func (s Store) writeHandoff(handoff ThreadHandoff) error {
	s = s.withDefaults()
	if err := s.ensureThreadDirs(); err != nil {
		return err
	}
	data, err := marshalHandoff(handoff)
	if err != nil {
		return err
	}
	if s.testHooks != nil && s.testHooks.writeHandoff != nil {
		if err := s.testHooks.writeHandoff(handoff); err != nil {
			return err
		}
	}
	return fileutil.WriteFileAtomic(s.handoffPath(handoff.ID), data, 0o644)
}

func marshalHandoff(handoff ThreadHandoff) ([]byte, error) {
	if strings.TrimSpace(handoff.ID) == "" {
		return nil, errors.New("threads: handoff id is empty")
	}
	return json.MarshalIndent(handoff, "", "  ")
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

type sessionThreadLinkChange struct {
	store           *memory.JSONLStore
	key             string
	before          memory.SessionMeta
	after           memory.SessionMeta
	sessionExisted  bool
	metadataExisted bool
}

func (c *sessionThreadLinkChange) rollback() error {
	if c == nil || c.store == nil || c.key == "" {
		return nil
	}
	var replacement *memory.SessionMeta
	if c.metadataExisted {
		before := c.before
		replacement = &before
	} else if !c.sessionExisted {
		deleted, err := c.store.CompareAndDeleteEmptySessionStrict(
			context.Background(),
			c.key,
			c.after,
		)
		if err != nil {
			return fmt.Errorf("threads: roll back empty session thread link: %w", err)
		}
		if !deleted {
			return errors.New(
				"threads: roll back empty session thread link: session changed concurrently",
			)
		}
		return nil
	}
	restored, err := c.store.CompareAndSwapSessionMetaStrict(
		context.Background(),
		c.key,
		c.after,
		replacement,
	)
	if err != nil {
		return fmt.Errorf("threads: roll back session thread link: %w", err)
	}
	if !restored {
		return errors.New("threads: roll back session thread link: metadata changed concurrently")
	}
	return nil
}

func rollbackThreadOperation(
	_ context.Context,
	operationErr error,
	artifacts []durableFileBackup,
	linkChange *sessionThreadLinkChange,
) error {
	errs := []error{operationErr}
	for index := len(artifacts) - 1; index >= 0; index-- {
		if err := artifacts[index].restore(); err != nil {
			errs = append(errs, fmt.Errorf("threads: roll back %s: %w", artifacts[index].path, err))
		}
	}
	if err := linkChange.rollback(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s Store) claimSessionThreadLink(
	ctx context.Context,
	sessionKey,
	threadID string,
	attachedAt time.Time,
	requireExisting bool,
) (*sessionThreadLinkChange, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil, errors.New("threads: session key is empty")
	}
	sessionStore, err := s.openSessionStore()
	if err != nil {
		return nil, err
	}
	change := &sessionThreadLinkChange{store: sessionStore}
	canonicalKey, _, err := sessionStore.UpdateSessionMetaStrict(
		ctx,
		sessionKey,
		func(meta *memory.SessionMeta, state memory.SessionMetaMutationState) error {
			if scopeErr := rejectReviewThreadScope(meta.Scope); scopeErr != nil {
				return scopeErr
			}
			if requireExisting && !state.SessionExists {
				return errSessionMissing
			}
			change.before = *meta
			change.sessionExisted = state.SessionExists
			change.metadataExisted = state.MetadataExists
			if meta.CreatedAt.IsZero() {
				meta.CreatedAt = attachedAt
			}
			meta.UpdatedAt = attachedAt
			meta.ThreadID = strings.TrimSpace(threadID)
			meta.ThreadAttachedAt = attachedAt
			change.after = *meta
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	change.key = canonicalKey
	return change, nil
}

func (s Store) setSessionThreadLink(
	ctx context.Context,
	sessionKey,
	threadID string,
	attachedAt time.Time,
) error {
	if strings.TrimSpace(sessionKey) == "" {
		return nil
	}
	_, err := s.claimSessionThreadLink(ctx, sessionKey, threadID, attachedAt, false)
	return err
}

func (s Store) clearSessionThreadLink(sessionKey string) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	sessionStore, err := s.openSessionStore()
	if err != nil {
		return err
	}
	_, _, err = sessionStore.UpdateSessionMetaStrict(
		context.Background(),
		sessionKey,
		func(meta *memory.SessionMeta, state memory.SessionMetaMutationState) error {
			if scopeErr := rejectReviewThreadScope(meta.Scope); scopeErr != nil {
				return scopeErr
			}
			if !state.SessionExists {
				return errSessionMissing
			}
			meta.ThreadID = ""
			meta.ThreadAttachedAt = time.Time{}
			meta.UpdatedAt = time.Now().UTC()
			return nil
		},
	)
	if errors.Is(err, errSessionMissing) {
		return nil
	}
	return err
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
	sessionStore, err := s.openSessionStore()
	if err != nil {
		return err
	}
	seenCanonical := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".meta.json")
		candidate, err := readMeta(
			filepath.Join(s.Dir, entry.Name()),
			sessionKeyFromSanitizedBase(base),
		)
		if err != nil {
			return err
		}
		candidateKey := candidate.Key
		canonicalKey, messages, meta, historyModifiedAt, found, err := sessionStore.ReadSessionStateStrict(
			ctx,
			candidateKey,
		)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		if !found {
			continue
		}
		// Review working contexts are private derived sessions, never threads.
		// Skip them before consulting legacy thread fields or writing registry
		// metadata, including when old metadata already contains those fields.
		if scopeErr := rejectReviewThreadScope(meta.Scope); scopeErr != nil {
			if errors.Is(scopeErr, errReviewScope) {
				continue
			}
			return scopeErr
		}
		meta.Key = canonicalKey
		if _, seen := seenCanonical[canonicalKey]; seen {
			continue
		}
		seenCanonical[canonicalKey] = struct{}{}
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
		if _, threadErr := s.readThreadMeta(threadID); threadErr == nil {
			continue
		}
		scope := scopeFromMeta(meta)
		title, _ := titleAndPreview(meta, visibleMessages(messages))
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
			if threadMeta.CreatedAt.IsZero() {
				threadMeta.CreatedAt = historyModifiedAt
			}
			if threadMeta.UpdatedAt.IsZero() {
				threadMeta.UpdatedAt = historyModifiedAt
			}
		}
		registryBackup, err := readDurableFileBackup(s.threadPath(threadID))
		if err != nil {
			return err
		}
		_, registryExpected, err := marshalThreadMeta(threadMeta)
		if err != nil {
			return err
		}
		registryBackup = registryBackup.expecting(registryExpected)
		var linkChange *sessionThreadLinkChange
		if meta.ThreadID == "" {
			linkChange, err = s.claimSessionThreadLink(
				ctx,
				meta.Key,
				threadID,
				time.Now().UTC(),
				true,
			)
			if err != nil {
				return err
			}
		}
		if err := s.writeThreadMeta(threadMeta); err != nil {
			if linkChange != nil {
				return rollbackThreadOperation(
					ctx,
					err,
					[]durableFileBackup{registryBackup},
					linkChange,
				)
			}
			return err
		}
	}
	_ = ctx
	return nil
}

func shouldMigrateSessionMeta(meta memory.SessionMeta) bool {
	if strings.TrimSpace(meta.Key) == "" {
		return false
	}
	if rejectReviewThreadScope(meta.Scope) != nil {
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
