package threads

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
		linkResult <- threadStore.setSessionThreadLink(
			context.Background(),
			allocation.Key,
			"thread-race",
			time.Now(),
		)
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

func TestThreadRegistryProjectionAndMigrationExcludeReviewSessions(t *testing.T) {
	t.Run("stale registry projection", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		reviewStore, reviewKey, reviewAlias, _ := seedReviewScopedSession(t, cfg)
		reviewBefore := readThreadTestSessionState(t, reviewStore, reviewKey)
		now := time.Now().UTC()
		meta := ThreadMeta{
			ID:                "stale-review-registry",
			UISessionID:       "private-review-ui-id",
			PrimarySessionKey: reviewAlias,
			Title:             "",
			SourceQuery:       "",
			SessionKeys:       []string{reviewAlias},
			Registration:      RegistrationMigrated,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := threadStore.writeThreadMeta(meta); err != nil {
			t.Fatalf("writeThreadMeta() error = %v", err)
		}

		if projected, ok := threadStore.threadFromRegistryMeta(meta); ok ||
			!reflect.DeepEqual(projected, Thread{}) {
			t.Fatalf("threadFromRegistryMeta() = (%#v, %v), want hidden", projected, ok)
		}
		items, err := threadStore.ListAll(ListOptions{IncludeDropped: true})
		if err != nil {
			t.Fatalf("ListAll() error = %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("ListAll() = %#v, want no review key/preview/count projection", items)
		}
		items, err = threadStore.Search(SearchOptions{
			Query:          "private review transcript",
			IncludeDropped: true,
		})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("Search() = %#v, want no private transcript result", items)
		}
		if projected, ok, err := threadStore.Get(meta.ID); err != nil || ok ||
			!reflect.DeepEqual(projected, Thread{}) {
			t.Fatalf("Get() = (%#v, %v, %v), want hidden", projected, ok, err)
		}
		assertThreadTestSessionState(t, reviewStore, reviewKey, reviewBefore)
	})

	t.Run("legacy metadata migration", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		reviewStore, reviewKey, _, _ := seedReviewScopedSession(t, cfg)
		const privateThreadID = "legacy-private-review-thread"
		if err := reviewStore.UpdateSessionMeta(context.Background(), reviewKey, func(meta *memory.SessionMeta) error {
			meta.ThreadID = privateThreadID
			meta.ThreadTitle = "private review title"
			meta.ThreadType = TypeReviewing
			meta.ThreadSourceQuery = "private review transcript"
			meta.ThreadContext = map[string]string{"review": "private"}
			return nil
		}); err != nil {
			t.Fatalf("UpdateSessionMeta() error = %v", err)
		}
		reviewBefore := readThreadTestSessionState(t, reviewStore, reviewKey)

		items, err := threadStore.ListAll(ListOptions{IncludeDropped: true})
		if err != nil {
			t.Fatalf("ListAll() error = %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("ListAll() = %#v, want no migrated review thread", items)
		}
		if _, err := os.Stat(threadStore.threadPath(privateThreadID)); !os.IsNotExist(err) {
			t.Fatalf("review migration registry stat error = %v, want not exist", err)
		}
		assertThreadTestSessionState(t, reviewStore, reviewKey, reviewBefore)
	})
}

func TestThreadMutationsArbitrateReviewAdmissionAfterPreflight(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		reviewScope, reviewKey, _ := reviewThreadTestIdentity("create-race")
		arrived := make(chan struct{})
		release := make(chan struct{})
		threadStore.testHooks = &threadStoreTestHooks{afterCreatePreflight: func() {
			close(arrived)
			<-release
		}}
		result := make(chan error, 1)
		go func() {
			_, err := threadStore.CreateThread(context.Background(), CreateRequest{
				ID:                "create-race-thread",
				PrimarySessionKey: reviewKey,
				Title:             "must not be registered",
			})
			result <- err
		}()
		waitForThreadTestSignal(t, arrived, "CreateThread preflight")

		reviewStore, err := memory.NewJSONLStore(ResolveSessionsDir(cfg.Agents.Defaults.Workspace))
		if err != nil {
			t.Fatalf("NewJSONLStore() error = %v", err)
		}
		reviewBefore := claimThreadTestReviewSession(
			t,
			reviewStore,
			reviewScope,
			[]providers.Message{{Role: "user", Content: "private create-race transcript"}},
		)
		close(release)
		if err := waitForThreadTestError(t, result, "CreateThread"); !errors.Is(err, errReviewScope) {
			t.Fatalf("CreateThread() error = %v, want %v", err, errReviewScope)
		}

		assertThreadTestSessionState(t, reviewStore, reviewKey, reviewBefore)
		if _, err := os.Stat(threadStore.threadPath("create-race-thread")); !os.IsNotExist(err) {
			t.Fatalf("raced thread registry stat error = %v, want not exist", err)
		}
		assertNoThreadTestArtifacts(t, threadStore.HandoffsDir)
	})

	t.Run("attach origin", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		target, err := threadStore.CreatePicoThread(context.Background(), cfg, CreateRequest{
			Title: "ordinary attach-race target",
		})
		if err != nil {
			t.Fatalf("CreatePicoThread() error = %v", err)
		}
		targetMetaBefore, err := threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta() error = %v", err)
		}
		targetStore, err := memory.NewJSONLStore(ResolveSessionsDir(cfg.Agents.Defaults.Workspace))
		if err != nil {
			t.Fatalf("NewJSONLStore(target) error = %v", err)
		}
		targetBefore := readThreadTestSessionState(t, targetStore, target.PrimarySessionKey)
		reviewScope, reviewKey, _ := reviewThreadTestIdentity("attach-origin-race")
		arrived := make(chan struct{})
		release := make(chan struct{})
		threadStore.testHooks = &threadStoreTestHooks{afterAttachPreflight: func() {
			close(arrived)
			<-release
		}}
		result := make(chan error, 1)
		go func() {
			_, _, attachErr := threadStore.AttachCurrent(context.Background(), AttachRequest{
				ThreadID:   target.ID,
				SessionKey: reviewKey,
				Summary:    "must not reach ordinary target history",
			})
			result <- attachErr
		}()
		waitForThreadTestSignal(t, arrived, "AttachCurrent origin preflight")

		reviewBefore := claimThreadTestReviewSession(
			t,
			targetStore,
			reviewScope,
			[]providers.Message{{Role: "user", Content: "private attach-origin transcript"}},
		)
		close(release)
		if resultErr := waitForThreadTestError(t, result, "AttachCurrent origin"); !errors.Is(
			resultErr,
			errReviewScope,
		) {
			t.Fatalf("AttachCurrent() error = %v, want %v", resultErr, errReviewScope)
		}

		assertThreadTestSessionState(t, targetStore, reviewKey, reviewBefore)
		assertThreadTestSessionState(t, targetStore, target.PrimarySessionKey, targetBefore)
		targetMetaAfter, err := threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta(after) error = %v", err)
		}
		if !reflect.DeepEqual(targetMetaAfter, targetMetaBefore) {
			t.Fatalf("target registry changed:\n before=%#v\n  after=%#v", targetMetaBefore, targetMetaAfter)
		}
		assertEmptyThreadTestDir(t, threadStore.HandoffsDir)
	})

	t.Run("attach primary", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		target, err := threadStore.CreatePicoThread(context.Background(), cfg, CreateRequest{
			Title: "target whose registry will be stale",
		})
		if err != nil {
			t.Fatalf("CreatePicoThread() error = %v", err)
		}
		sessionStore, err := memory.NewJSONLStore(ResolveSessionsDir(cfg.Agents.Defaults.Workspace))
		if err != nil {
			t.Fatalf("NewJSONLStore() error = %v", err)
		}
		originalTargetBefore := readThreadTestSessionState(t, sessionStore, target.PrimarySessionKey)
		reviewScope, reviewKey, reviewAlias := reviewThreadTestIdentity("attach-primary-race")
		targetMetaBefore, err := threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta() error = %v", err)
		}
		targetMetaBefore.PrimarySessionKey = reviewAlias
		targetMetaBefore.SessionKeys = []string{reviewAlias}
		if writeErr := threadStore.writeThreadMeta(targetMetaBefore); writeErr != nil {
			t.Fatalf("writeThreadMeta(stale target) error = %v", writeErr)
		}
		targetMetaBefore, err = threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta(stale target) error = %v", err)
		}
		originKey := session.BuildOpaqueSessionKey("attach-primary-race-origin")
		seedSlottedSession(
			t,
			ResolveSessionsDir(cfg.Agents.Defaults.Workspace),
			originKey,
			json.RawMessage(`{"version":1}`),
			nil,
			nil,
			[]providers.Message{{Role: "user", Content: "ordinary origin history"}},
		)
		originBefore := readThreadTestSessionState(t, sessionStore, originKey)
		arrived := make(chan struct{})
		release := make(chan struct{})
		threadStore.testHooks = &threadStoreTestHooks{afterAttachPreflight: func() {
			close(arrived)
			<-release
		}}
		result := make(chan error, 1)
		go func() {
			_, _, attachErr := threadStore.AttachCurrent(context.Background(), AttachRequest{
				ThreadID:   target.ID,
				SessionKey: originKey,
				Summary:    "must not enter protected primary history",
			})
			result <- attachErr
		}()
		waitForThreadTestSignal(t, arrived, "AttachCurrent primary preflight")

		reviewBefore := claimThreadTestReviewSession(
			t,
			sessionStore,
			reviewScope,
			[]providers.Message{{Role: "user", Content: "private attach-primary transcript"}},
		)
		close(release)
		if resultErr := waitForThreadTestError(t, result, "AttachCurrent primary"); !errors.Is(
			resultErr,
			errReviewScope,
		) {
			t.Fatalf("AttachCurrent() error = %v, want %v", resultErr, errReviewScope)
		}

		assertThreadTestSessionState(t, sessionStore, reviewKey, reviewBefore)
		assertThreadTestSessionState(t, sessionStore, originKey, originBefore)
		assertThreadTestSessionState(t, sessionStore, target.PrimarySessionKey, originalTargetBefore)
		targetMetaAfter, err := threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta(after) error = %v", err)
		}
		if !reflect.DeepEqual(targetMetaAfter, targetMetaBefore) {
			t.Fatalf("target registry changed:\n before=%#v\n  after=%#v", targetMetaBefore, targetMetaAfter)
		}
		assertEmptyThreadTestDir(t, threadStore.HandoffsDir)
	})
}

func TestThreadMetadataMutationsRejectReviewScopedSessions(t *testing.T) {
	t.Run("register guessed alias", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		reviewStore, reviewKey, reviewAlias, reviewScope := seedReviewScopedSession(t, cfg)
		before := readThreadTestSessionState(t, reviewStore, reviewKey)

		_, err := threadStore.RegisterCurrent(context.Background(), CreateRequest{
			ID:                "forbidden-register",
			PrimarySessionKey: reviewAlias,
		}, nil)
		if !errors.Is(err, errReviewScope) {
			t.Fatalf("RegisterCurrent() error = %v, want %v", err, errReviewScope)
		}
		assertThreadTestSessionState(t, reviewStore, reviewKey, before)
		if _, statErr := os.Stat(threadStore.threadPath("forbidden-register")); !os.IsNotExist(statErr) {
			t.Fatalf("registered thread file stat error = %v, want not exist", statErr)
		}

		_, err = threadStore.RegisterCurrent(context.Background(), CreateRequest{
			ID:                "forbidden-scope-register",
			PrimarySessionKey: session.BuildOpaqueSessionKey("ordinary-register-key"),
		}, &reviewScope)
		if !errors.Is(err, errReviewScope) {
			t.Fatalf("RegisterCurrent(review scope) error = %v, want %v", err, errReviewScope)
		}
		if _, statErr := os.Stat(threadStore.threadPath("forbidden-scope-register")); !os.IsNotExist(statErr) {
			t.Fatalf("scope-registered thread file stat error = %v, want not exist", statErr)
		}
	})

	t.Run("attach review origin", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		target, err := threadStore.CreatePicoThread(context.Background(), cfg, CreateRequest{
			Title: "ordinary target",
		})
		if err != nil {
			t.Fatalf("CreatePicoThread() error = %v", err)
		}
		targetMetaBefore, err := threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta() error = %v", err)
		}
		targetStore, err := memory.NewJSONLStore(ResolveSessionsDir(cfg.Agents.Defaults.Workspace))
		if err != nil {
			t.Fatalf("NewJSONLStore(target) error = %v", err)
		}
		targetBefore := readThreadTestSessionState(t, targetStore, target.PrimarySessionKey)
		reviewStore, reviewKey, reviewAlias, _ := seedReviewScopedSession(t, cfg)
		reviewBefore := readThreadTestSessionState(t, reviewStore, reviewKey)

		_, _, err = threadStore.AttachCurrent(context.Background(), AttachRequest{
			ThreadID:   target.ID,
			SessionKey: reviewAlias,
			Summary:    "must not reach the target",
		})
		if !errors.Is(err, errReviewScope) {
			t.Fatalf("AttachCurrent() error = %v, want %v", err, errReviewScope)
		}
		assertThreadTestSessionState(t, reviewStore, reviewKey, reviewBefore)
		assertThreadTestSessionState(t, targetStore, target.PrimarySessionKey, targetBefore)
		targetMetaAfter, err := threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta(after) error = %v", err)
		}
		if !reflect.DeepEqual(targetMetaAfter, targetMetaBefore) {
			t.Fatalf("target thread metadata changed:\n before=%#v\n  after=%#v", targetMetaBefore, targetMetaAfter)
		}
		assertEmptyThreadTestDir(t, threadStore.HandoffsDir)
	})

	t.Run("attach review primary", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		target, err := threadStore.CreatePicoThread(context.Background(), cfg, CreateRequest{
			Title: "target redirected at review session",
		})
		if err != nil {
			t.Fatalf("CreatePicoThread() error = %v", err)
		}
		reviewStore, reviewKey, reviewAlias, _ := seedReviewScopedSession(t, cfg)
		reviewBefore := readThreadTestSessionState(t, reviewStore, reviewKey)
		targetMetaBefore, err := threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta() error = %v", err)
		}
		targetMetaBefore.PrimarySessionKey = reviewAlias
		if writeErr := threadStore.writeThreadMeta(targetMetaBefore); writeErr != nil {
			t.Fatalf("writeThreadMeta() error = %v", writeErr)
		}
		targetMetaBefore, err = threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta(rewritten) error = %v", err)
		}
		originKey := session.BuildOpaqueSessionKey("thread-review-guard-origin")
		originStore := seedSlottedSession(
			t,
			ResolveSessionsDir(cfg.Agents.Defaults.Workspace),
			originKey,
			json.RawMessage(`{}`),
			nil,
			nil,
			[]providers.Message{{Role: "user", Content: "ordinary origin"}},
		)
		originBefore := readThreadTestSessionState(t, originStore, originKey)

		_, _, err = threadStore.AttachCurrent(context.Background(), AttachRequest{
			ThreadID:   target.ID,
			SessionKey: originKey,
			Summary:    "must not enter review history",
		})
		if !errors.Is(err, errReviewScope) {
			t.Fatalf("AttachCurrent() error = %v, want %v", err, errReviewScope)
		}
		assertThreadTestSessionState(t, reviewStore, reviewKey, reviewBefore)
		assertThreadTestSessionState(t, originStore, originKey, originBefore)
		targetMetaAfter, err := threadStore.readThreadMeta(target.ID)
		if err != nil {
			t.Fatalf("readThreadMeta(after) error = %v", err)
		}
		if !reflect.DeepEqual(targetMetaAfter, targetMetaBefore) {
			t.Fatalf("target thread metadata changed:\n before=%#v\n  after=%#v", targetMetaBefore, targetMetaAfter)
		}
		assertEmptyThreadTestDir(t, threadStore.HandoffsDir)
	})

	t.Run("detach", func(t *testing.T) {
		cfg := testConfig(t)
		threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		reviewStore, reviewKey, reviewAlias, _ := seedReviewScopedSession(t, cfg)
		attachedAt := time.Unix(123, 0).UTC()
		if err := reviewStore.UpdateSessionMeta(context.Background(), reviewKey, func(meta *memory.SessionMeta) error {
			meta.ThreadID = "existing-private-link"
			meta.ThreadAttachedAt = attachedAt
			return nil
		}); err != nil {
			t.Fatalf("UpdateSessionMeta(seed link) error = %v", err)
		}
		before := readThreadTestSessionState(t, reviewStore, reviewKey)

		err := threadStore.DetachCurrent(reviewAlias)
		if !errors.Is(err, errReviewScope) {
			t.Fatalf("DetachCurrent() error = %v, want %v", err, errReviewScope)
		}
		assertThreadTestSessionState(t, reviewStore, reviewKey, before)
	})
}

func TestStrictThreadProjectionAndUpdateRejectCorruptAliasCatalog(t *testing.T) {
	cfg := testConfig(t)
	threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	thread, err := threadStore.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Title: "ordinary thread before catalog corruption",
	})
	if err != nil {
		t.Fatal(err)
	}
	registryPath := threadStore.threadPath(thread.ID)
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(
		filepath.Join(threadStore.Dir, "corrupt-alias-owner.meta.json"),
		[]byte("not-json"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	meta, err := threadStore.readThreadMeta(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if projected, ok := threadStore.threadFromRegistryMeta(meta); ok ||
		!reflect.DeepEqual(projected, Thread{}) {
		t.Fatalf("corrupt-catalog projection = (%#v, %v), want hidden", projected, ok)
	}
	if _, ok, updateErr := threadStore.UpdateThread(
		thread.ID,
		UpdateRequest{Title: "must not persist"},
	); updateErr == nil || ok || !strings.Contains(updateErr.Error(), "decode session metadata") {
		t.Fatalf("UpdateThread(corrupt catalog) = (ok=%v, err=%v)", ok, updateErr)
	}
	registryAfter, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(registryAfter, registryBefore) {
		t.Fatal("rejected corrupt-catalog update changed registry")
	}
	if _, err := threadStore.List(); err == nil {
		t.Fatalf("List(corrupt alias catalog) error = %v", err)
	}
}

func TestReturnToOriginRevalidatesAuthoritativeSessions(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(*testing.T, Store, *memory.JSONLStore, ThreadHandoff, Thread)
		wantErr error
	}{
		{
			name: "origin became review",
			corrupt: func(t *testing.T, _ Store, sessionStore *memory.JSONLStore, handoff ThreadHandoff, _ Thread) {
				t.Helper()
				reviewScope, _, _ := reviewThreadTestIdentity("stale-handoff-origin")
				if err := sessionStore.UpdateSessionMeta(
					context.Background(),
					handoff.OriginSessionKey,
					func(meta *memory.SessionMeta) error {
						meta.Scope = mustMarshalScope(t, reviewScope)
						return nil
					},
				); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: errReviewScope,
		},
		{
			name: "origin metadata corrupt",
			corrupt: func(t *testing.T, store Store, _ *memory.JSONLStore, handoff ThreadHandoff, _ Thread) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(store.Dir, sanitizeSessionKey(handoff.OriginSessionKey)+".meta.json"),
					[]byte("not-json"),
					0o644,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "target became review",
			corrupt: func(t *testing.T, _ Store, sessionStore *memory.JSONLStore, _ ThreadHandoff, target Thread) {
				t.Helper()
				reviewScope, _, _ := reviewThreadTestIdentity("stale-handoff-target")
				if err := sessionStore.UpdateSessionMeta(
					context.Background(),
					target.PrimarySessionKey,
					func(meta *memory.SessionMeta) error {
						meta.Scope = mustMarshalScope(t, reviewScope)
						return nil
					},
				); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: errReviewScope,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
			target, err := threadStore.CreatePicoThread(context.Background(), cfg, CreateRequest{
				Title: "stale handoff target",
			})
			if err != nil {
				t.Fatal(err)
			}
			originKey := session.BuildOpaqueSessionKey("stale-handoff-origin-" + test.name)
			sessionStore := seedSlottedSession(
				t,
				threadStore.Dir,
				originKey,
				json.RawMessage(`{"version":1,"channel":"pico"}`),
				nil,
				nil,
				[]providers.Message{{Role: "user", Content: "ordinary origin"}},
			)
			_, handoff, err := threadStore.AttachCurrent(context.Background(), AttachRequest{
				ThreadID:   target.ID,
				SessionKey: originKey,
			})
			if err != nil {
				t.Fatal(err)
			}
			test.corrupt(t, threadStore, sessionStore, handoff, target)

			got, ok, err := threadStore.ReturnToOrigin(handoff.ID)
			if ok || !reflect.DeepEqual(got, ThreadHandoff{}) {
				t.Fatalf("ReturnToOrigin(stale) = (%#v, %v, %v), want hidden", got, ok, err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("ReturnToOrigin(stale) error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && err == nil {
				t.Fatal("ReturnToOrigin(corrupt) error = nil")
			}
		})
	}
}

func TestUpdateThreadRejectsStaleReviewPrimaryWithoutRegistryMutation(t *testing.T) {
	cfg := testConfig(t)
	threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	target, err := threadStore.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Title: "registry redirected later",
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewStore, reviewKey, reviewAlias, _ := seedReviewScopedSession(t, cfg)
	reviewBefore := readThreadTestSessionState(t, reviewStore, reviewKey)
	meta, err := threadStore.readThreadMeta(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta.PrimarySessionKey = reviewAlias
	if writeErr := threadStore.writeThreadMeta(meta); writeErr != nil {
		t.Fatal(writeErr)
	}
	path := threadStore.threadPath(target.ID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := threadStore.UpdateThread(target.ID, UpdateRequest{Title: "must not persist"})
	if !errors.Is(err, errReviewScope) || ok || !reflect.DeepEqual(got, Thread{}) {
		t.Fatalf("UpdateThread(review primary) = (%#v, %v, %v)", got, ok, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after, before) {
		t.Fatal("rejected update changed stale registry")
	}
	assertThreadTestSessionState(t, reviewStore, reviewKey, reviewBefore)
}

func TestAttachReviewOriginRaceWithMissingPrimaryMutatesNothing(t *testing.T) {
	cfg := testConfig(t)
	threadStore := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	target, err := threadStore.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Title: "target registry made stale",
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := threadStore.readThreadMeta(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	missingPrimary := session.BuildOpaqueSessionKey("missing-attach-primary")
	meta.PrimarySessionKey = missingPrimary
	meta.SessionKeys = []string{missingPrimary}
	if writeErr := threadStore.writeThreadMeta(meta); writeErr != nil {
		t.Fatal(writeErr)
	}
	registryBefore, err := os.ReadFile(threadStore.threadPath(target.ID))
	if err != nil {
		t.Fatal(err)
	}
	reviewScope, reviewKey, _ := reviewThreadTestIdentity("missing-primary-origin-race")
	arrived := make(chan struct{})
	release := make(chan struct{})
	threadStore.testHooks = &threadStoreTestHooks{afterAttachPreflight: func() {
		close(arrived)
		<-release
	}}
	result := make(chan error, 1)
	go func() {
		_, _, attachErr := threadStore.AttachCurrent(context.Background(), AttachRequest{
			ThreadID:   target.ID,
			SessionKey: reviewKey,
			Summary:    "must not project",
		})
		result <- attachErr
	}()
	waitForThreadTestSignal(t, arrived, "AttachCurrent missing-primary race")
	sessionStore, err := memory.NewJSONLStore(threadStore.Dir)
	if err != nil {
		t.Fatal(err)
	}
	reviewBefore := claimThreadTestReviewSession(
		t,
		sessionStore,
		reviewScope,
		[]providers.Message{{Role: "user", Content: "private raced origin"}},
	)
	close(release)
	if resultErr := waitForThreadTestError(
		t,
		result,
		"AttachCurrent missing-primary race",
	); !errors.Is(resultErr, errSessionMissing) {
		t.Fatalf("AttachCurrent() error = %v, want %v", resultErr, errSessionMissing)
	}
	assertThreadTestSessionState(t, sessionStore, reviewKey, reviewBefore)
	if _, _, _, found, readErr := sessionStore.ReadSessionSnapshot(
		context.Background(),
		missingPrimary,
	); readErr != nil || found {
		t.Fatalf("missing primary snapshot = (found=%v, err=%v)", found, readErr)
	}
	registryAfter, err := os.ReadFile(threadStore.threadPath(target.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(registryAfter, registryBefore) {
		t.Fatal("missing-primary race changed registry")
	}
	assertEmptyThreadTestDir(t, threadStore.HandoffsDir)
}

func TestThreadCreateAndAttachFailuresRollBackAllState(t *testing.T) {
	injected := errors.New("injected thread artifact write failure")
	t.Run("pico create registry write", func(t *testing.T) {
		cfg := testConfig(t)
		store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		threadID := "rollback-pico-create"
		allocation := AllocatePicoThread(cfg, threadID)
		store.testHooks = &threadStoreTestHooks{writeThreadMeta: func(ThreadMeta) error {
			return injected
		}}
		_, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
			ID:    threadID,
			Title: "must roll back pico metadata",
		})
		if !errors.Is(err, injected) {
			t.Fatalf("CreatePicoThread() error = %v", err)
		}
		sessionStore, openErr := memory.NewJSONLStore(store.Dir)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if slices.Contains(sessionStore.ListSessions(), allocation.Key) {
			t.Fatalf("failed pico create left discoverable session %q", allocation.Key)
		}
		if _, _, _, found, readErr := sessionStore.ReadSessionSnapshot(
			context.Background(),
			allocation.Key,
		); readErr != nil || found {
			t.Fatalf("failed pico create session artifacts = (found=%v, err=%v)", found, readErr)
		}
		if _, found, getErr := store.GetMeta(allocation.SessionID); getErr != nil || found {
			t.Fatalf("failed pico create registry = (found=%v, err=%v)", found, getErr)
		}
	})

	t.Run("create registry write after missing-session claim", func(t *testing.T) {
		cfg := testConfig(t)
		store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
		key := session.BuildOpaqueSessionKey("rollback-create-missing")
		store.testHooks = &threadStoreTestHooks{writeThreadMeta: func(ThreadMeta) error {
			return injected
		}}
		_, err := store.CreateThread(context.Background(), CreateRequest{
			ID:                "rollback-create",
			PrimarySessionKey: key,
			Title:             "must roll back",
		})
		if !errors.Is(err, injected) {
			t.Fatalf("CreateThread() error = %v", err)
		}
		sessionStore, openErr := memory.NewJSONLStore(store.Dir)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, _, _, found, readErr := sessionStore.ReadSessionSnapshot(
			context.Background(),
			key,
		); readErr != nil || found {
			t.Fatalf("rolled-back create session = (found=%v, err=%v)", found, readErr)
		}
		if _, statErr := os.Stat(store.threadPath("rollback-create")); !os.IsNotExist(statErr) {
			t.Fatalf("rolled-back registry stat error = %v", statErr)
		}
	})

	for _, failure := range []string{"registry", "handoff"} {
		t.Run("attach "+failure+" write", func(t *testing.T) {
			cfg := testConfig(t)
			store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
			target, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
				Title: "rollback attach target",
			})
			if err != nil {
				t.Fatal(err)
			}
			originKey := session.BuildOpaqueSessionKey("rollback-attach-origin-" + failure)
			sessionStore := seedSlottedSession(
				t,
				store.Dir,
				originKey,
				json.RawMessage(`{"version":1,"channel":"pico"}`),
				nil,
				nil,
				[]providers.Message{{Role: "user", Content: "origin before failure"}},
			)
			originBefore := readThreadTestSessionState(t, sessionStore, originKey)
			targetBefore := readThreadTestSessionState(t, sessionStore, target.PrimarySessionKey)
			registryPath := store.threadPath(target.ID)
			registryBefore, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			store.testHooks = &threadStoreTestHooks{}
			if failure == "registry" {
				store.testHooks.writeThreadMeta = func(ThreadMeta) error { return injected }
			} else {
				store.testHooks.writeHandoff = func(ThreadHandoff) error { return injected }
			}
			_, _, err = store.AttachCurrent(context.Background(), AttachRequest{
				ThreadID:   target.ID,
				SessionKey: originKey,
				Summary:    "must not remain",
			})
			if !errors.Is(err, injected) {
				t.Fatalf("AttachCurrent() error = %v", err)
			}
			assertThreadTestSessionState(t, sessionStore, originKey, originBefore)
			assertThreadTestSessionState(t, sessionStore, target.PrimarySessionKey, targetBefore)
			registryAfter, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(registryAfter, registryBefore) {
				t.Fatal("failed attach changed registry")
			}
			assertEmptyThreadTestDir(t, store.HandoffsDir)
		})
	}
}

func TestAttachSummaryProjectionFailureIsPostCommitBestEffort(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	target, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Title: "summary failure target",
	})
	if err != nil {
		t.Fatal(err)
	}
	originKey := session.BuildOpaqueSessionKey("summary-failure-origin")
	sessionStore := seedSlottedSession(
		t,
		store.Dir,
		originKey,
		json.RawMessage(`{"version":1,"channel":"pico"}`),
		nil,
		nil,
		[]providers.Message{{Role: "user", Content: "origin"}},
	)
	targetBefore := readThreadTestSessionState(t, sessionStore, target.PrimarySessionKey)
	injected := errors.New("injected summary projection failure")
	store.testHooks = &threadStoreTestHooks{appendSummary: func(
		context.Context,
		string,
		providers.Message,
	) error {
		return injected
	}}
	attached, handoff, err := store.AttachCurrent(context.Background(), AttachRequest{
		ThreadID:   target.ID,
		SessionKey: originKey,
		Summary:    "best effort summary",
	})
	if err != nil || attached.ID != target.ID || handoff.ID == "" {
		t.Fatalf("AttachCurrent(summary failure) = (%#v, %#v, %v)", attached, handoff, err)
	}
	targetAfter := readThreadTestSessionState(t, sessionStore, target.PrimarySessionKey)
	if !reflect.DeepEqual(targetAfter, targetBefore) {
		t.Fatalf("failed best-effort summary mutated target:\n before=%#v\n  after=%#v", targetBefore, targetAfter)
	}
	if returned, ok, err := store.ReturnToOrigin(handoff.ID); err != nil || !ok || returned.ID != handoff.ID {
		t.Fatalf("ReturnToOrigin(committed attach) = (%#v, %v, %v)", returned, ok, err)
	}
}

type threadTestSessionState struct {
	canonicalKey string
	history      []providers.Message
	meta         memory.SessionMeta
}

func seedReviewScopedSession(
	t *testing.T,
	cfg *config.Config,
) (*memory.JSONLStore, string, string, session.SessionScope) {
	t.Helper()
	scope, key, alias := reviewThreadTestIdentity("case-thread-guard")
	store := seedSlottedSession(
		t,
		ResolveSessionsDir(cfg.Agents.Defaults.Workspace),
		key,
		mustMarshalScope(t, scope),
		[]string{alias},
		nil,
		[]providers.Message{{Role: "user", Content: "private review transcript"}},
	)
	return store, key, alias, scope
}

func reviewThreadTestIdentity(caseID string) (session.SessionScope, string, string) {
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"review"},
		Values:     map[string]string{"review": caseID},
	}
	return scope, session.BuildSessionKey(scope), "review:agent:main:case:" + caseID
}

func claimThreadTestReviewSession(
	t *testing.T,
	store *memory.JSONLStore,
	scope session.SessionScope,
	history []providers.Message,
) threadTestSessionState {
	t.Helper()
	key := session.BuildSessionKey(scope)
	alias := "review:agent:main:case:" + scope.Values["review"]
	backend := session.NewJSONLBackend(store)
	updated, err := backend.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
		Key:            key,
		Scope:          &scope,
		InitialAliases: []string{alias},
		Mode:           session.ScopeAdmissionReview,
	})
	if err != nil || !updated {
		t.Fatalf("AdmitSessionScope(review) = (updated=%v, err=%v)", updated, err)
	}
	reserved, found, err := backend.ReadSessionSnapshot(context.Background(), alias)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(reservation) = (found=%v, err=%v)", found, err)
	}
	if err := backend.ReplaceSessionSnapshot(context.Background(), session.SessionSnapshotReplacement{
		Key:              key,
		History:          history,
		Summary:          "private review summary",
		Scope:            &scope,
		Aliases:          []string{alias},
		ExpectedRevision: reserved.Revision,
	}); err != nil {
		t.Fatalf("ReplaceSessionSnapshot(review) error = %v", err)
	}
	return readThreadTestSessionState(t, store, key)
}

func waitForThreadTestSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not reach deterministic barrier", operation)
	}
}

func waitForThreadTestError(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not complete", operation)
		return nil
	}
}

func assertNoThreadTestArtifacts(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("ReadDir(%q) = %#v, want no artifacts", dir, entries)
	}
}

func readThreadTestSessionState(
	t *testing.T,
	store *memory.JSONLStore,
	key string,
) threadTestSessionState {
	t.Helper()
	canonicalKey, history, meta, found, err := store.ReadSessionSnapshot(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(%q) = (found=%v, err=%v)", key, found, err)
	}
	return threadTestSessionState{canonicalKey: canonicalKey, history: history, meta: meta}
}

func assertThreadTestSessionState(
	t *testing.T,
	store *memory.JSONLStore,
	key string,
	want threadTestSessionState,
) {
	t.Helper()
	got := readThreadTestSessionState(t, store, key)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session %q changed:\n want=%#v\n  got=%#v", key, want, got)
	}
}

func assertEmptyThreadTestDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("ReadDir(%q) = %#v, want empty", dir, entries)
	}
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
