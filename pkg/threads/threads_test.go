package threads

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	cfg.Session.Dimensions = []string{"chat"}
	return cfg
}

func TestCreatePicoThreadPersistsSearchableContext(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)

	thread, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeCoding,
		Title:       "Implement launcher tabs",
		SourceQuery: "code in /extra/dkropachev/picoclaw repo: git@github.com:dkropachev/picoclaw.git branch main",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread() error = %v", err)
	}

	if thread.ID == "" {
		t.Fatal("thread ID is empty")
	}
	if thread.Type != TypeCoding {
		t.Fatalf("thread.Type = %q, want %q", thread.Type, TypeCoding)
	}
	if got := thread.Context["location"]; got != "/extra/dkropachev/picoclaw" {
		t.Fatalf("location context = %q", got)
	}
	if got := thread.Context["repo"]; got != "git@github.com:dkropachev/picoclaw.git" {
		t.Fatalf("repo context = %q", got)
	}
	if got := thread.Context["branch"]; got != "main" {
		t.Fatalf("branch context = %q", got)
	}

	items, err := store.Search(SearchOptions{Query: "/extra/dkropachev/picoclaw", Type: TypeCoding})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != thread.ID {
		t.Fatalf("Search() = %#v, want created thread", items)
	}
}

func TestCreatePicoThreadDefaultsSourceQuery(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)

	thread, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{})
	if err != nil {
		t.Fatalf("CreatePicoThread() error = %v", err)
	}
	if thread.SourceQuery == "" {
		t.Fatal("thread.SourceQuery is empty")
	}
	if thread.SourceQuery != "New thread" {
		t.Fatalf("thread.SourceQuery = %q, want New thread", thread.SourceQuery)
	}
}

func TestSearchRanksUpdatedThreadAndFiltersContext(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)

	coding, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeCoding,
		Title:       "Picoclaw coding",
		Context:     map[string]string{"location": "/extra/dkropachev/picoclaw"},
		SourceQuery: "picoclaw coding",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread(coding) error = %v", err)
	}
	_, err = store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeReviewing,
		Title:       "Release PR review",
		Context:     map[string]string{"pr": "42"},
		SourceQuery: "review release pr",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread(review) error = %v", err)
	}

	items, err := store.Search(SearchOptions{
		Query: "location:/extra/dkropachev/picoclaw",
		Type:  TypeCoding,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != coding.ID {
		t.Fatalf("Search() = %#v, want coding thread", items)
	}
}

func TestDropThreadHidesFromDiscoveryButPreservesDirectLookup(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)

	thread, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeGeneral,
		Title:       "Japan travel",
		SourceQuery: "find me a thread regarding japan",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread() error = %v", err)
	}

	dropped, ok, err := store.DropThread(thread.UISessionID)
	if err != nil {
		t.Fatalf("DropThread() error = %v", err)
	}
	if !ok {
		t.Fatal("DropThread() ok = false")
	}
	if dropped.Discoverable || dropped.DroppedAt == nil {
		t.Fatalf("dropped thread = %#v, want non-discoverable with timestamp", dropped)
	}

	items, err := store.Search(SearchOptions{Query: "japan"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("Search() = %#v, want no discoverable threads", items)
	}

	all, err := store.Search(SearchOptions{Query: "japan", IncludeDropped: true})
	if err != nil {
		t.Fatalf("Search(include dropped) error = %v", err)
	}
	if len(all) != 1 || all[0].ID != thread.ID || all[0].Discoverable {
		t.Fatalf("Search(include dropped) = %#v, want dropped thread", all)
	}

	loaded, ok, err := store.Get(thread.UISessionID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok || loaded.ID != thread.ID || loaded.Discoverable {
		t.Fatalf("Get() = %#v, ok=%v; want dropped thread by session id", loaded, ok)
	}
}

func TestListIncludesExistingPicoSessionMetadata(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	allocation := AllocatePicoThread(cfg, "session-existing")
	store.UpsertSessionMeta(
		context.Background(),
		allocation.Key,
		mustMarshalScope(t, allocation.Scope),
		allocation.Aliases,
	)
	if addErr := store.AddFullMessage(context.Background(), allocation.Key, providers.Message{
		Role:    "user",
		Content: "Investigate a websocket regression",
	}); addErr != nil {
		t.Fatalf("AddFullMessage() error = %v", addErr)
	}

	items, err := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace).Search(SearchOptions{
		Query: "websocket regression",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "session-existing" {
		t.Fatalf("items[0].ID = %q, want session-existing", items[0].ID)
	}
	if items[0].Type != TypeInvestigating {
		t.Fatalf("items[0].Type = %q, want %q", items[0].Type, TypeInvestigating)
	}
}

func TestListMigratesPreviewAndCountFromActiveHistorySlot(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	allocation := AllocatePicoThread(cfg, "session-slotted")
	active := []providers.Message{
		{Role: "user", Content: "Investigate the active slot regression"},
		{Role: "assistant", Content: "The active slot contains the current answer."},
	}
	seedSlottedSession(
		t,
		dir,
		allocation.Key,
		mustMarshalScope(t, allocation.Scope),
		allocation.Aliases,
		[]providers.Message{{Role: "user", Content: "stale legacy preview"}},
		active,
	)

	items, err := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace).Search(SearchOptions{
		Query: "active slot regression",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "session-slotted" {
		t.Fatalf("items[0].ID = %q, want session-slotted", items[0].ID)
	}
	if items[0].Preview != active[0].Content {
		t.Fatalf("items[0].Preview = %q, want %q", items[0].Preview, active[0].Content)
	}
	if items[0].MessageCount != len(active) {
		t.Fatalf("items[0].MessageCount = %d, want %d", items[0].MessageCount, len(active))
	}

	assertSlottedSession(t, dir, allocation.Key, active)
}

func TestCreatePicoThreadPreservesActiveHistorySlot(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	allocation := AllocatePicoThread(cfg, "session-create-slotted")
	active := []providers.Message{{Role: "user", Content: "Keep this active-slot history"}}
	committedScope := json.RawMessage(`{"version":1,"owner":"snapshot-replacement"}`)
	committedAliases := append(
		append([]string(nil), allocation.Aliases...),
		"review:preserved-alias",
	)
	seedSlottedSession(
		t,
		dir,
		allocation.Key,
		committedScope,
		committedAliases,
		[]providers.Message{{Role: "user", Content: "discarded legacy history"}},
		active,
	)

	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	thread, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		ID:    allocation.SessionID,
		Title: "Existing slotted thread",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread() error = %v", err)
	}
	if thread.Preview != active[0].Content {
		t.Fatalf("thread.Preview = %q, want %q", thread.Preview, active[0].Content)
	}
	if thread.MessageCount != len(active) {
		t.Fatalf("thread.MessageCount = %d, want %d", thread.MessageCount, len(active))
	}
	assertSlottedSession(t, dir, allocation.Key, active)
	sessionStore, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, meta, found, err := sessionStore.ReadSessionSnapshot(
		context.Background(),
		allocation.Key,
	)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	var storedScope map[string]any
	if err := json.Unmarshal(meta.Scope, &storedScope); err != nil {
		t.Fatal(err)
	}
	if storedScope["owner"] != "snapshot-replacement" ||
		!slices.Equal(meta.Aliases, committedAliases) ||
		meta.Summary != "" || meta.ThreadTitle != "Existing slotted thread" {
		t.Fatalf("CreatePicoThread() clobbered replacement-owned metadata: %+v", meta)
	}

	if err := store.DetachCurrent(allocation.Key); err != nil {
		t.Fatalf("DetachCurrent() error = %v", err)
	}
	assertSlottedSession(t, dir, allocation.Key, active)
}

func TestListPrefersPromotedCanonicalSessionOverLegacyShadow(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	allocation := AllocatePicoThread(cfg, "session-promoted-shadow")
	legacyKey := ""
	for _, alias := range allocation.Aliases {
		if strings.Contains(alias, allocation.SessionID) {
			legacyKey = alias
			break
		}
	}
	if legacyKey == "" {
		t.Fatalf("Pico allocation has no session alias: %v", allocation.Aliases)
	}
	sessionStore, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rawScope := mustMarshalScope(t, allocation.Scope)
	if addErr := sessionStore.AddMessage(
		context.Background(),
		legacyKey,
		"user",
		"stale legacy thread preview",
	); addErr != nil {
		t.Fatal(addErr)
	}
	if upsertErr := sessionStore.UpsertSessionMeta(
		context.Background(),
		allocation.Key,
		rawScope,
		allocation.Aliases,
	); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	if promoted, promoteErr := sessionStore.PromoteAliasHistory(
		context.Background(),
		allocation.Key,
		rawScope,
		allocation.Aliases,
	); promoteErr != nil || !promoted {
		t.Fatalf("PromoteAliasHistory() = (%v, %v)", promoted, promoteErr)
	}
	_, _, before, found, err := sessionStore.ReadSessionSnapshot(
		context.Background(),
		allocation.Key,
	)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	if replaceErr := sessionStore.ReplaceSessionSnapshot(
		context.Background(),
		memory.SessionSnapshotReplacement{
			Key:              allocation.Key,
			History:          []providers.Message{{Role: "user", Content: "new canonical thread preview"}},
			Summary:          "new canonical summary",
			Scope:            rawScope,
			Aliases:          append([]string(nil), allocation.Aliases...),
			ExpectedRevision: before.Revision,
		},
	); replaceErr != nil {
		t.Fatal(replaceErr)
	}

	threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	items, err := threadStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].PrimarySessionKey != allocation.Key ||
		items[0].Preview != "new canonical thread preview" {
		t.Fatalf("promoted thread list = %#v", items)
	}
	_, _, canonicalMeta, found, err := sessionStore.ReadSessionSnapshot(
		context.Background(),
		allocation.Key,
	)
	if err != nil || !found || canonicalMeta.ThreadID != items[0].ID {
		t.Fatalf("canonical migrated link = (found=%v, meta=%+v, err=%v)", found, canonicalMeta, err)
	}
	legacyMeta, err := readMeta(
		filepath.Join(dir, sanitizeSessionKey(legacyKey)+".meta.json"),
		legacyKey,
	)
	if err != nil || legacyMeta.ThreadID != "" {
		t.Fatalf("legacy shadow link = %+v, err=%v", legacyMeta, err)
	}
	if detachErr := threadStore.DetachCurrent(legacyKey); detachErr != nil {
		t.Fatal(detachErr)
	}
	canonicalMeta, err = sessionStore.GetSessionMeta(context.Background(), allocation.Key)
	if err != nil || canonicalMeta.ThreadID != "" {
		t.Fatalf("canonical detached link = %+v, err=%v", canonicalMeta, err)
	}
	legacyMeta, err = readMeta(
		filepath.Join(dir, sanitizeSessionKey(legacyKey)+".meta.json"),
		legacyKey,
	)
	if err != nil || legacyMeta.ThreadID != "" {
		t.Fatalf("detach mutated legacy shadow = %+v, err=%v", legacyMeta, err)
	}

	// Simulate a registry record written before canonical-key migration. A
	// handoff must canonicalize it before appending the transfer summary.
	registryMeta, err := threadStore.readThreadMeta(items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	registryMeta.PrimarySessionKey = legacyKey
	registryMeta.SessionKeys = []string{legacyKey}
	if writeErr := threadStore.writeThreadMeta(registryMeta); writeErr != nil {
		t.Fatal(writeErr)
	}
	originKey := session.BuildOpaqueSessionKey("promoted-shadow-origin")
	seedSlottedSession(
		t,
		dir,
		originKey,
		json.RawMessage(`{"version":1}`),
		nil,
		nil,
		[]providers.Message{{Role: "user", Content: "origin"}},
	)
	activeSlot := canonicalMeta.HistorySlot
	_, handoff, err := threadStore.AttachCurrent(context.Background(), AttachRequest{
		ThreadID:        items[0].ID,
		SessionKey:      originKey,
		OriginSessionID: "origin",
		Summary:         "canonical handoff summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.TargetThreadID != items[0].ID {
		t.Fatalf("handoff = %+v", handoff)
	}
	canonicalHistory, canonicalMeta, _, err := sessionStore.ReadSessionState(
		context.Background(),
		allocation.Key,
	)
	if err != nil || len(canonicalHistory) != 2 ||
		!strings.Contains(canonicalHistory[1].Content, "canonical handoff summary") ||
		canonicalMeta.HistorySlot != activeSlot {
		t.Fatalf("canonical handoff tuple = history=%+v meta=%+v err=%v", canonicalHistory, canonicalMeta, err)
	}
	legacyData, err := os.ReadFile(filepath.Join(dir, sanitizeSessionKey(legacyKey)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var legacyMessage providers.Message
	if decodeErr := json.Unmarshal(
		[]byte(strings.TrimSpace(string(legacyData))),
		&legacyMessage,
	); decodeErr != nil ||
		legacyMessage.Content != "stale legacy thread preview" {
		t.Fatalf("legacy shadow changed = message=%+v err=%v", legacyMessage, decodeErr)
	}
	registryMeta, err = threadStore.readThreadMeta(items[0].ID)
	if err != nil || registryMeta.PrimarySessionKey != allocation.Key {
		t.Fatalf("canonicalized registry meta = %+v, err=%v", registryMeta, err)
	}
}

func TestListToleratesMalformedRecordInActiveHistorySlot(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	allocation := AllocatePicoThread(cfg, "session-slotted-recovery")
	active := []providers.Message{{Role: "user", Content: "Recover the valid active record"}}
	seedSlottedSession(
		t,
		dir,
		allocation.Key,
		mustMarshalScope(t, allocation.Scope),
		allocation.Aliases,
		nil,
		active,
	)
	slotPath := filepath.Join(dir, sanitizeSessionKey(allocation.Key)+".history-a")
	valid, err := json.Marshal(active[0])
	if err != nil {
		t.Fatalf("Marshal(message) error = %v", err)
	}
	contents := append([]byte("{malformed\n"), valid...)
	contents = append(contents, '\n')
	if writeErr := os.WriteFile(slotPath, contents, 0o644); writeErr != nil {
		t.Fatalf("WriteFile(%q) error = %v", slotPath, writeErr)
	}

	items, err := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace).Search(SearchOptions{
		Query: "valid active record",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 1 || items[0].Preview != active[0].Content {
		t.Fatalf("Search() = %#v, want recovered active-slot preview", items)
	}
}

func TestSessionThreadLinkAndSnapshotReplacementDoNotClobber(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	allocation := AllocatePicoThread(cfg, "session-slot-link-race")
	sessionStore := seedSlottedSession(
		t,
		dir,
		allocation.Key,
		mustMarshalScope(t, allocation.Scope),
		allocation.Aliases,
		nil,
		[]providers.Message{{Role: "user", Content: "old snapshot"}},
	)
	_, _, meta, found, err := sessionStore.ReadSessionSnapshot(context.Background(), allocation.Key)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	replacement := memory.SessionSnapshotReplacement{
		Key:              allocation.Key,
		History:          []providers.Message{{Role: "assistant", Content: "new snapshot"}},
		Scope:            mustMarshalScope(t, allocation.Scope),
		Aliases:          append([]string(nil), allocation.Aliases...),
		ExpectedRevision: meta.Revision,
	}
	threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	start := make(chan struct{})
	linkResult := make(chan error, 1)
	replaceResult := make(chan error, 1)
	go func() {
		<-start
		linkResult <- threadStore.setSessionThreadLink(allocation.Key, "thread-race", time.Now())
	}()
	go func() {
		<-start
		replaceResult <- sessionStore.ReplaceSessionSnapshot(context.Background(), replacement)
	}()
	close(start)
	if linkErr := <-linkResult; linkErr != nil {
		t.Fatalf("setSessionThreadLink() error = %v", linkErr)
	}
	if replaceErr := <-replaceResult; replaceErr != nil {
		if !errors.Is(replaceErr, memory.ErrSnapshotConflict) {
			t.Fatalf("ReplaceSessionSnapshot() error = %v", replaceErr)
		}
		_, _, current, found, readErr := sessionStore.ReadSessionSnapshot(
			context.Background(),
			allocation.Key,
		)
		if readErr != nil || !found {
			t.Fatalf("retry snapshot read = (found=%v, err=%v)", found, readErr)
		}
		replacement.ExpectedRevision = current.Revision
		if retryErr := sessionStore.ReplaceSessionSnapshot(
			context.Background(),
			replacement,
		); retryErr != nil {
			t.Fatalf("replacement retry error = %v", retryErr)
		}
	}
	history, committed, _, err := sessionStore.ReadSessionState(context.Background(), allocation.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "new snapshot" || committed.ThreadID != "thread-race" {
		t.Fatalf("coordinated tuple = history=%+v meta=%+v", history, committed)
	}
}

func TestListExcludesPlainOpaqueSessions(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	key := session.BuildOpaqueSessionKey("agent:main:test:plain")
	if addErr := store.AddFullMessage(context.Background(), key, providers.Message{
		Role:    "user",
		Content: "plain transport session",
	}); addErr != nil {
		t.Fatalf("AddFullMessage() error = %v", addErr)
	}

	items, err := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace).List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("List() = %#v, want no registered threads", items)
	}
}

func TestAttachCurrentLinksSessionAndCreatesHandoff(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	thread, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeCoding,
		Title:       "Fix CI",
		SourceQuery: "fix CI",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread() error = %v", err)
	}

	currentKey := session.BuildOpaqueSessionKey("agent:main:pico:direct:other")
	active := []providers.Message{{Role: "user", Content: "current slotted session"}}
	seedSlottedSession(
		t,
		ResolveSessionsDir(cfg.Agents.Defaults.Workspace),
		currentKey,
		nil,
		nil,
		[]providers.Message{{Role: "user", Content: "stale current session"}},
		active,
	)
	attached, handoff, err := store.AttachCurrent(context.Background(), AttachRequest{
		ThreadID:        thread.ID,
		SessionKey:      currentKey,
		OriginSessionID: "other",
		Summary:         "User clarified this is the CI thread.",
	})
	if err != nil {
		t.Fatalf("AttachCurrent() error = %v", err)
	}
	if attached.ID != thread.ID {
		t.Fatalf("attached.ID = %q, want %q", attached.ID, thread.ID)
	}
	if handoff.TargetSessionID != thread.UISessionID {
		t.Fatalf("handoff.TargetSessionID = %q, want %q", handoff.TargetSessionID, thread.UISessionID)
	}

	metaStore, err := memory.NewJSONLStore(ResolveSessionsDir(cfg.Agents.Defaults.Workspace))
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	meta, err := metaStore.GetSessionMeta(context.Background(), currentKey)
	if err != nil {
		t.Fatalf("GetSessionMeta() error = %v", err)
	}
	if meta.ThreadID != thread.ID {
		t.Fatalf("meta.ThreadID = %q, want %q", meta.ThreadID, thread.ID)
	}
	assertSlottedSession(t, ResolveSessionsDir(cfg.Agents.Defaults.Workspace), currentKey, active)
}

func seedSlottedSession(
	t *testing.T,
	dir string,
	key string,
	scope json.RawMessage,
	aliases []string,
	legacy []providers.Message,
	active []providers.Message,
) *memory.JSONLStore {
	t.Helper()
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	expectedRevision := ""
	if len(legacy) > 0 {
		if err := store.EnsureSessionHistory(context.Background(), key); err != nil {
			t.Fatalf("EnsureSessionHistory() error = %v", err)
		}
		for _, message := range legacy {
			if err := store.AddFullMessage(context.Background(), key, message); err != nil {
				t.Fatalf("AddFullMessage() error = %v", err)
			}
		}
		_, _, meta, found, err := store.ReadSessionSnapshot(context.Background(), key)
		if err != nil || !found {
			t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
		}
		expectedRevision = meta.Revision
	}
	if len(scope) == 0 {
		scope = json.RawMessage(`{}`)
	}
	if err := store.ReplaceSessionSnapshot(context.Background(), memory.SessionSnapshotReplacement{
		Key:              key,
		History:          active,
		Scope:            append(json.RawMessage(nil), scope...),
		Aliases:          append([]string(nil), aliases...),
		ExpectedRevision: expectedRevision,
	}); err != nil {
		t.Fatalf("ReplaceSessionSnapshot() error = %v", err)
	}
	return store
}

func assertSlottedSession(t *testing.T, dir, key string, want []providers.Message) {
	t.Helper()
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	history, meta, _, err := store.ReadSessionState(context.Background(), key)
	if err != nil {
		t.Fatalf("ReadSessionState() error = %v", err)
	}
	if meta.HistorySlot != "a" {
		t.Fatalf("meta.HistorySlot = %q, want a", meta.HistorySlot)
	}
	if len(history) != len(want) {
		t.Fatalf("len(history) = %d, want %d", len(history), len(want))
	}
	for i := range want {
		if history[i].Role != want[i].Role || history[i].Content != want[i].Content {
			t.Fatalf("history[%d] = %#v, want %#v", i, history[i], want[i])
		}
	}
}

func mustMarshalScope(t *testing.T, scope any) []byte {
	t.Helper()
	data, err := json.Marshal(scope)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return data
}
