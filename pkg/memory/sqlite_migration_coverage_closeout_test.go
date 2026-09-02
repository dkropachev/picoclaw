//nolint:govet // Independent migration assertions intentionally use narrow error scopes.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestLegacySessionMigrationEnumerationAndClassificationBoundaries(t *testing.T) {
	workspace := t.TempDir()
	for _, directory := range []string{"sessions", "threads", "threads/handoffs"} {
		if err := os.MkdirAll(filepath.Join(workspace, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string][]byte{
		"sessions/one.json":           []byte(`{"key":"one"}`),
		"sessions/two.jsonl":          []byte(`{"role":"user","content":"two"}` + "\n"),
		"sessions/ignore.txt":         []byte("ignored"),
		"threads/thread.json":         []byte(`{"id":"thread"}`),
		"threads/handoffs/one.json":   []byte(`{"id":"handoff"}`),
		"sessions/sessions.db":        nil,
		"sessions/sessions.db-wal":    nil,
		"sessions/three.history-a":    nil,
		"sessions/three.history-b":    nil,
		"sessions/four.json.migrated": nil,
	} {
		path := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := enumerateLegacySessionSources(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 7 {
		t.Fatalf("legacy source count = %d: %#v", len(sources), sources)
	}
	for index := 1; index < len(sources); index++ {
		if sources[index-1].Relative > sources[index].Relative {
			t.Fatal("legacy sources are not sorted")
		}
	}

	for relative, want := range map[string]bool{
		"sessions/a.json":          true,
		"sessions/a.json.migrated": true,
		"sessions/a.jsonl":         true,
		"sessions/a.history-a":     true,
		"sessions/a.history-b":     true,
		"threads/a.json":           true,
		"sessions/sessions.db":     false,
		"sessions/sessions.db-wal": false,
		"sessions/a.txt":           false,
		"other/a.json":             false,
	} {
		if got := isLegacySessionRelative(relative); got != want {
			t.Errorf("isLegacySessionRelative(%q) = %t, want %t", relative, got, want)
		}
	}
	info, err := os.Stat(filepath.Join(workspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyEnumerationDirectory(info); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(workspace, "sessions"), 0o722); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(filepath.Join(workspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyEnumerationDirectory(info); err == nil {
		t.Fatal("writable legacy directory accepted")
	}
}

func TestLegacySessionOptionsCaptureAndFinalizeFences(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyTestFile(t, workspace, "sessions/source.json", []byte(`{"key":"source"}`))
	options, err := newSessionsLegacyOptions(dir)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := options.Sources()
	if err != nil || len(sources) != 1 || sources[0].Relative != "sessions/source.json" {
		t.Fatalf("captured legacy sources = %#v, %v", sources, err)
	}
	sources[0].Relative = "mutated"
	again, err := options.Sources()
	if err != nil || again[0].Relative != "sessions/source.json" {
		t.Fatalf("detached legacy sources = %#v, %v", again, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	input := sqlitestore.LegacyInput{ID: "source", Relative: "sessions/source.json", Data: []byte(`{}`)}
	if _, err := options.Import(canceled, nil, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled legacy capture error = %v", err)
	}
	if _, err := options.Import(t.Context(), nil, input); err != nil {
		t.Fatal(err)
	}
	if _, err := options.FinalizeResults(t.Context(), nil, sqlitestore.LegacyFinalizeInput{
		SourceIDs: []string{"missing"},
	}); err == nil {
		t.Fatal("missing captured source finalized")
	}
	if err := validateLegacyEnumerationDirectory(nil); err == nil {
		t.Fatal("nil legacy directory metadata accepted")
	}
}

func TestLegacyEnumerationRejectsNestedAliasAndWritableDirectory(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "sessions", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, "sessions", "nested", "alias.json")); err == nil {
		if _, err := enumerateLegacySessionSources(workspace); err == nil {
			t.Fatal("nested legacy symlink accepted")
		}
	}

	workspace = t.TempDir()
	nested := filepath.Join(workspace, "sessions", "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nested, 0o722); err != nil {
		t.Fatal(err)
	}
	if _, err := enumerateLegacySessionSources(workspace); err == nil {
		t.Fatal("writable nested legacy directory accepted")
	}
}

func TestLegacySessionMigrationDecodersAndAuditBoundaries(t *testing.T) {
	if count, err := countLegacyHistoryRecords([]byte("\n one \n\n two\n")); err != nil || count != 2 {
		t.Fatalf("history record count = %d, %v", count, err)
	}
	if _, err := countLegacyHistoryRecords(bytes.Repeat([]byte("x"), maxLineSize+1)); err == nil {
		t.Fatal("oversized history line was accepted")
	}
	var decoded map[string]any
	if err := decodeSingleLegacyJSON([]byte(`{"ok":true}`), &decoded); err != nil || decoded["ok"] != true {
		t.Fatalf("decoded legacy JSON = %#v, %v", decoded, err)
	}
	for _, raw := range [][]byte{nil, []byte(`{`), []byte(`{} {}`)} {
		if err := decodeSingleLegacyJSON(raw, &decoded); err == nil {
			t.Fatalf("invalid legacy JSON accepted: %q", raw)
		}
	}

	source := sqlitestore.LegacyInput{ID: "source", Relative: "sessions/source.jsonl"}
	audit := newLegacySessionImportAudit([]sqlitestore.LegacyInput{source})
	audit.imported(source, 2)
	audit.skipped(source, "ignored", nil, 0)
	for index := 0; index < 520; index++ {
		audit.skipped(source, "invalid", []byte{byte(index)}, 1)
	}
	result := audit.results[source.ID]
	if result.Imported != 2 || result.Skipped != 520 || len(result.Issues) != 512 {
		t.Fatalf("legacy audit = %#v", result)
	}

	validLines := []byte(
		`{"role":"user","content":"one"}` + "\n" +
			`not-json` + "\n" +
			`{"role":"assistant","content":"two"}` + "\n",
	)
	source.Data = validLines
	history, count, err := decodeLegacyHistoryForImport(source, 1, audit)
	if err != nil || count != 1 || len(history) != 1 || history[0].Content != "two" {
		t.Fatalf("decoded legacy history = %#v count:%d err:%v", history, count, err)
	}
	filtered := filterAggregateLegacyMessages([]providers.Message{
		{Role: "user", Content: "one"},
		{Role: strings.Repeat("x", 1025), Content: "invalid"},
		{Role: "assistant", Content: "two"},
	}, source, audit)
	if len(filtered) != 2 {
		t.Fatalf("filtered aggregate messages = %#v", filtered)
	}
	oversized := source
	oversized.Data = bytes.Repeat([]byte("x"), maxLineSize+1)
	if _, _, err := decodeLegacyHistoryForImport(oversized, 0, audit); err == nil {
		t.Fatal("oversized legacy history line accepted")
	}
	transient := providers.Message{Role: "assistant", ReasoningContent: "thought"}
	encodedTransient, err := json.Marshal(transient)
	if err != nil {
		t.Fatal(err)
	}
	transientSource := source
	transientSource.Data = append(encodedTransient, '\n')
	if history, count, err := decodeLegacyHistoryForImport(
		transientSource,
		0,
		audit,
	); err != nil || count != 0 ||
		len(history) != 0 {
		t.Fatalf("transient legacy history = %#v count:%d err:%v", history, count, err)
	}
	if got := filterAggregateLegacyMessages([]providers.Message{transient}, source, audit); len(got) != 0 {
		t.Fatalf("transient aggregate history = %#v", got)
	}
}

func TestLegacySessionMigrationMetadataValidationBoundaries(t *testing.T) {
	now := time.Now().UTC()
	valid := SessionMeta{
		Key: " key ", Summary: "summary", CreatedAt: now, UpdatedAt: now,
		Scope: json.RawMessage(
			`{"version":1,"agent_id":"main","channel":"pico","dimensions":["sender"],"values":{"sender":"user"}}`,
		),
		Aliases: []string{" alias ", "alias"}, ThreadID: "thread", ThreadType: "coding",
		ThreadTitle: "title", ThreadContext: map[string]string{"repo": "owner/repo"},
	}
	prepared, err := prepareLegacySessionMeta(valid)
	if err != nil || prepared.Key != "key" || len(prepared.Aliases) != 1 {
		t.Fatalf("prepared legacy metadata = %#v, %v", prepared, err)
	}
	for name, meta := range map[string]SessionMeta{
		"blank key":        {},
		"negative skip":    {Key: "key", Skip: -1},
		"skip after count": {Key: "key", Skip: 2, Count: 1},
		"history selector": {Key: "key", HistorySlot: "c"},
		"bad scope":        {Key: "key", Scope: json.RawMessage(`{`)},
		"bad context":      {Key: "key", ThreadContext: map[string]string{"": "value"}},
		"bad alias":        {Key: "key", Aliases: []string{strings.Repeat("a", 16_385)}},
		"bad scope dimension": {
			Key: "key", Scope: json.RawMessage(
				`{"version":1,"dimensions":["missing"],"values":{}}`,
			),
		},
		"duplicate scope dimension": {
			Key: "key", Scope: json.RawMessage(
				`{"version":1,"dimensions":["d","d"],"values":{"d":"v"}}`,
			),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareLegacySessionMeta(meta); err == nil {
				t.Fatal("invalid legacy metadata accepted")
			}
		})
	}

	invalidTime := time.Unix(253_402_300_800, 0).UTC()
	if err := validateLegacyMessage(providers.Message{
		Role: "user", Content: "content", CreatedAt: &invalidTime,
	}); err == nil {
		t.Fatal("invalid legacy message timestamp accepted")
	}
	if err := validateLegacyMessage(providers.Message{
		Role: strings.Repeat("r", 1025), Content: "content",
	}); err == nil {
		t.Fatal("oversized legacy message accepted")
	}
}

func TestLegacyThreadAndHandoffValidationBoundaries(t *testing.T) {
	now := time.Now().UTC()
	thread := legacyThreadMeta{
		ID: " thread ", PrimarySessionKey: " session ", Type: "implementation",
		Registration: "unknown", SessionKeys: []string{"session", "other", "other"},
		Aliases: []string{"alias", "alias"}, Context: map[string]string{"repo": "owner/repo"},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := validateLegacyThreadMeta(&thread); err != nil || thread.ID != "thread" ||
		thread.Type != "coding" || thread.Registration != "migrated" || len(thread.SessionKeys) != 2 {
		t.Fatalf("legacy thread = %#v, %v", thread, err)
	}
	for input, want := range map[string]string{
		"code": "coding", "pr": "reviewing", "debug": "investigating", "other": "general",
	} {
		if got := normalizeLegacyThreadType(input); got != want {
			t.Errorf("normalizeLegacyThreadType(%q) = %q", input, got)
		}
	}
	for input, want := range map[string]string{"AUTO": "auto", "bad": "migrated"} {
		if got := normalizeLegacyThreadRegistration(input); got != want {
			t.Errorf("normalizeLegacyThreadRegistration(%q) = %q", input, got)
		}
	}
	if err := validateLegacyThreadMeta(&legacyThreadMeta{}); err == nil {
		t.Fatal("empty legacy thread accepted")
	}

	handoff := legacyThreadHandoff{
		ID: " handoff ", OriginSessionKey: " origin ", TargetThreadID: " thread ", CreatedAt: now,
	}
	if err := validateLegacyHandoff(&handoff); err != nil || handoff.ID != "handoff" {
		t.Fatalf("legacy handoff = %#v, %v", handoff, err)
	}
	if err := validateLegacyHandoff(&legacyThreadHandoff{}); err == nil {
		t.Fatal("empty legacy handoff accepted")
	}
	if validateLegacyText("ok", 1, 2) != nil || validateLegacyText("long", 1, 2) == nil {
		t.Fatal("legacy text validation boundary failed")
	}
	if validateLegacyTimestamp(now) != nil || validateLegacyTimestamp(time.Unix(253_402_300_800, 0)) == nil {
		t.Fatal("legacy timestamp validation boundary failed")
	}
	marker := &struct{}{}
	markerErr := json.Unmarshal([]byte(`{`), marker)
	if firstNonNilLegacyValidation(nil, markerErr) == nil || firstNonNilLegacyValidation(nil, nil) != nil {
		t.Fatal("legacy validation error selection failed")
	}
	if legacySessionKeyFromSanitizedBase("sk_v1_KEEP") != "sk_v1_KEEP" ||
		legacySessionKeyFromSanitizedBase("agent_main") != "agent:main" {
		t.Fatal("legacy session key normalization failed")
	}
	if firstLegacyValue(" ", " selected ", "later") != "selected" || firstLegacyValue(" ") != "" {
		t.Fatal("legacy first-value selection failed")
	}
	unique := uniqueLegacyStrings([]string{" one ", "", "one", "two"})
	if len(unique) != 2 || unique[0] != "one" || unique[1] != "two" {
		t.Fatalf("unique legacy strings = %#v", unique)
	}
}

func TestLegacyMigrationRelationalInsertBoundaries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workspace", "sessions")
	store, err := openLocalSQLiteStore(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	conn, err := store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	now := time.Now().UTC()
	if inserted, err := insertLegacySession(
		t.Context(), conn, " ", nil, SessionMeta{},
	); err != nil || inserted {
		t.Fatalf("blank legacy session = %t, %v", inserted, err)
	}
	meta := SessionMeta{Key: "session", CreatedAt: now, UpdatedAt: now, Aliases: []string{"alias"}}
	if inserted, err := insertLegacySession(
		t.Context(), conn, "session", []providers.Message{{Role: "user", Content: "one"}}, meta,
	); err != nil || !inserted {
		t.Fatalf("legacy session insert = %t, %v", inserted, err)
	}
	if inserted, err := insertLegacySession(t.Context(), conn, "session", nil, meta); err != nil || inserted {
		t.Fatalf("duplicate legacy session = %t, %v", inserted, err)
	}

	if inserted, reason, err := insertLegacyThread(
		t.Context(), conn, legacyThreadMeta{},
	); err != nil || inserted || reason != "invalid-thread-record" {
		t.Fatalf("blank legacy thread = %t, %q, %v", inserted, reason, err)
	}
	if inserted, reason, err := insertLegacyThread(t.Context(), conn, legacyThreadMeta{
		ID: "missing", PrimarySessionKey: "missing",
	}); err != nil || inserted || reason != "broken-thread-reference" {
		t.Fatalf("broken legacy thread = %t, %q, %v", inserted, reason, err)
	}
	thread := legacyThreadMeta{
		ID: "thread", PrimarySessionKey: "session", SessionKeys: []string{"session", "missing"},
		Aliases: []string{"thread-alias"}, Context: map[string]string{"repo": "owner/repo"},
		AttachedAt: map[string]time.Time{"session": now}, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := conn.ExecContext(t.Context(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if inserted, reason, err := insertLegacyThread(t.Context(), conn, thread); err != nil || !inserted || reason != "" {
		_, _ = conn.ExecContext(t.Context(), "ROLLBACK")
		t.Fatalf("legacy thread insert = %t, %q, %v", inserted, reason, err)
	}
	if _, err := conn.ExecContext(t.Context(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	inserted, reason, err := insertLegacyThread(t.Context(), conn, thread)
	if err != nil || inserted || reason != "thread-identity-conflict" {
		t.Fatalf("duplicate legacy thread = %t, %q, %v", inserted, reason, err)
	}

	if inserted, reason, err := insertLegacyHandoff(
		t.Context(), conn, legacyThreadHandoff{ID: "broken", OriginSessionKey: "missing", TargetThreadID: "thread"},
	); err != nil || inserted || reason != "broken-handoff-reference" {
		t.Fatalf("broken legacy handoff = %t, %q, %v", inserted, reason, err)
	}
	handoff := legacyThreadHandoff{
		ID: "handoff", OriginSessionKey: "session", TargetThreadID: "thread", CreatedAt: now,
	}
	inserted, reason, err = insertLegacyHandoff(t.Context(), conn, handoff)
	if err != nil || !inserted || reason != "" {
		t.Fatalf("legacy handoff insert = %t, %q, %v", inserted, reason, err)
	}
	inserted, reason, err = insertLegacyHandoff(t.Context(), conn, handoff)
	if err != nil || inserted || reason != "handoff-identity-conflict" {
		t.Fatalf("duplicate legacy handoff = %t, %q, %v", inserted, reason, err)
	}
}

func TestLegacyMigrationAuditsDeletedMissingAndConflictingInputs(t *testing.T) {
	workspace, dir := privateSessionsFixture(t)
	if err := os.MkdirAll(filepath.Join(workspace, "threads", "handoffs"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeLegacyJSONTestFile(t, workspace, "sessions/.session-delete-v1-invalid.json", sessionDeleteManifest{
		Version: 2, Keys: []string{"ignored"},
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/.session-delete-v1-empty.json", sessionDeleteManifest{
		Version: 1,
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/.session-delete-v1-good.json", sessionDeleteManifest{
		Version: 1, Keys: []string{"deleted", "deleted-history"},
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/deleted.json", jsonSession{
		Key: "deleted", Messages: []providers.Message{{Role: "user", Content: "deleted"}},
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/deleted.meta.json", SessionMeta{
		Key: "deleted", Count: 0,
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/selector.meta.json", SessionMeta{
		Key: "selector", HistorySlot: "invalid",
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/missing.meta.json", SessionMeta{
		Key: "missing", Count: 1,
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/inconsistent.meta.json", SessionMeta{
		Key: "inconsistent", Count: 2, CreatedAt: now, UpdatedAt: now,
	})
	writeLegacyLinesTestFile(t, workspace, "sessions/inconsistent.jsonl", []string{
		`{"role":"user","content":"one"}`,
	})
	writeLegacyLinesTestFile(t, workspace, "sessions/deleted-history.jsonl", []string{
		`{"role":"user","content":"deleted"}`,
	})
	writeLegacyLinesTestFile(t, workspace, "sessions/orphan.history-a", []string{
		`{"role":"user","content":"orphan"}`,
	})
	writeLegacyTestFile(t, workspace, "sessions/fallback.json", []byte(
		`{"messages":[{"role":"user","content":"fallback"}]}`,
	))
	writeLegacyJSONTestFile(t, workspace, "sessions/thread-session.json", jsonSession{
		Key: "thread-session", Messages: []providers.Message{{Role: "user", Content: "thread"}},
	})
	thread := legacyThreadMeta{
		ID: "duplicate-thread", PrimarySessionKey: "thread-session", CreatedAt: now, UpdatedAt: now,
	}
	writeLegacyJSONTestFile(t, workspace, "threads/a-thread.json", thread)
	writeLegacyJSONTestFile(t, workspace, "threads/b-thread.json", thread)
	writeLegacyJSONTestFile(t, workspace, "threads/broken-reference.json", legacyThreadMeta{
		ID: "broken-thread", PrimarySessionKey: "missing-session", CreatedAt: now, UpdatedAt: now,
	})
	writeLegacyJSONTestFile(t, workspace, "threads/handoffs/invalid.json", legacyThreadHandoff{})
	writeLegacyJSONTestFile(t, workspace, "threads/handoffs/broken.json", legacyThreadHandoff{
		ID: "broken-handoff", OriginSessionKey: "missing-session",
		TargetThreadID: "duplicate-thread", CreatedAt: now,
	})

	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var issueCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM storage_import_issues`).Scan(&issueCount); err != nil {
		t.Fatal(err)
	}
	if issueCount < 10 {
		t.Fatalf("edge migration issue count = %d", issueCount)
	}
	if history, err := store.GetHistory(t.Context(), "fallback"); err != nil || len(history) != 1 {
		t.Fatalf("fallback aggregate history = %#v, %v", history, err)
	}
}

func TestLegacyMigrationReconstructsMetaProjectedThreadRelationships(t *testing.T) {
	workspace, dir := privateSessionsFixture(t)
	if err := os.MkdirAll(filepath.Join(workspace, "threads", "handoffs"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index, key := range []string{"projected-one", "projected-two"} {
		meta := SessionMeta{
			Key: key, Count: 1, HistorySlot: "a", ThreadID: "projected-thread",
			ThreadTitle: "Projected", ThreadType: "coding", ThreadContext: map[string]string{"repo": "x"},
			ThreadAttachedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		writeLegacyJSONTestFile(t, workspace, "sessions/"+key+".meta.json", meta)
		writeLegacyLinesTestFile(t, workspace, "sessions/"+key+".history-a", []string{
			`{"role":"user","content":"message-` + string(rune('a'+index)) + `"}`,
		})
	}
	writeLegacyJSONTestFile(t, workspace, "threads/projected.json", legacyThreadMeta{
		ID: "projected-thread", PrimarySessionKey: "projected-one", CreatedAt: now, UpdatedAt: now,
	})
	writeLegacyJSONTestFile(t, workspace, "threads/handoffs/projected.json", legacyThreadHandoff{
		ID: "projected-handoff", OriginSessionKey: "projected-two",
		TargetThreadID: "projected-thread", CreatedAt: now,
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/conflict.json", jsonSession{
		Key: "conflict", Messages: []providers.Message{{Role: "user", Content: "aggregate"}},
	})
	writeLegacyLinesTestFile(t, workspace, "sessions/conflict.jsonl", []string{
		`{"role":"user","content":"history"}`,
	})

	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM threads WHERE thread_id = 'projected-thread'`:           1,
		`SELECT COUNT(*) FROM thread_sessions WHERE thread_id = 'projected-thread'`:   2,
		`SELECT COUNT(*) FROM thread_handoffs WHERE handoff_id = 'projected-handoff'`: 1,
	} {
		var count int
		if err := store.db.QueryRow(query).Scan(&count); err != nil || count != want {
			t.Fatalf("%s = %d, %v; want %d", query, count, err, want)
		}
	}
}

func TestLegacyInsertHelpersPropagateMissingRelationFaults(t *testing.T) {
	now := time.Now().UTC()
	scope := json.RawMessage(`{"version":1,"channel":"pico","values":{}}`)
	for name, test := range map[string]struct {
		breakSQL string
		meta     SessionMeta
		history  []providers.Message
	}{
		"sessions": {`DROP TABLE sessions`, SessionMeta{Key: "key"}, nil},
		"scope": {
			`DROP TABLE session_scopes`, SessionMeta{Key: "key", Scope: scope}, nil,
		},
		"aliases": {
			`DROP TABLE session_aliases`, SessionMeta{Key: "key", Aliases: []string{"alias"}}, nil,
		},
		"messages": {
			`DROP TABLE session_messages`,
			SessionMeta{Key: "key"},
			[]providers.Message{{Role: "user", Content: "one"}},
		},
	} {
		t.Run("session "+name, func(t *testing.T) {
			store, conn := newSessionSchemaCoverageConn(t)
			if _, err := store.db.Exec(test.breakSQL); err != nil {
				t.Fatal(err)
			}
			test.meta.CreatedAt, test.meta.UpdatedAt = now, now
			if inserted, err := insertLegacySession(
				t.Context(), conn, "key", test.history, test.meta,
			); err == nil || inserted {
				t.Fatalf("missing %s relation = inserted:%t err:%v", name, inserted, err)
			}
		})
	}
	for name, relation := range map[string]string{
		"threads": `threads`, "context": `thread_context`, "aliases": `thread_aliases`,
	} {
		t.Run("thread "+name, func(t *testing.T) {
			store, conn := newSessionSchemaCoverageConn(t)
			if err := store.EnsureSessionHistory(t.Context(), "session"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`PRAGMA foreign_keys = OFF; DROP TABLE ` + relation); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), `BEGIN`); err != nil {
				t.Fatal(err)
			}
			inserted, _, err := insertLegacyThread(t.Context(), conn, legacyThreadMeta{
				ID: "thread", PrimarySessionKey: "session", Context: map[string]string{"repo": "x"},
				Aliases: []string{"alias"}, CreatedAt: now, UpdatedAt: now,
			})
			_, _ = conn.ExecContext(t.Context(), `ROLLBACK`)
			if err == nil || inserted {
				t.Fatalf("missing %s relation = inserted:%t err:%v", name, inserted, err)
			}
		})
	}
	t.Run("handoff sessions query", func(t *testing.T) {
		store, conn := newSessionSchemaCoverageConn(t)
		if _, err := store.db.Exec(`DROP TABLE sessions`); err != nil {
			t.Fatal(err)
		}
		if inserted, _, err := insertLegacyHandoff(t.Context(), conn, legacyThreadHandoff{
			ID: "handoff", OriginSessionKey: "origin", TargetThreadID: "thread",
		}); err == nil || inserted {
			t.Fatalf("missing session relation = inserted:%t err:%v", inserted, err)
		}
	})
	t.Run("handoff threads query", func(t *testing.T) {
		store, conn := newSessionSchemaCoverageConn(t)
		if err := store.EnsureSessionHistory(t.Context(), "origin"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`PRAGMA foreign_keys = OFF; DROP TABLE threads`); err != nil {
			t.Fatal(err)
		}
		if inserted, _, err := insertLegacyHandoff(t.Context(), conn, legacyThreadHandoff{
			ID: "handoff", OriginSessionKey: "origin", TargetThreadID: "thread",
		}); err == nil || inserted {
			t.Fatalf("missing thread relation = inserted:%t err:%v", inserted, err)
		}
	})
}

func TestFinalizeLegacySessionsAuditAndDeleteFailureBoundaries(t *testing.T) {
	store, conn := newSessionSchemaCoverageConn(t)
	sources := []sqlitestore.LegacyInput{
		{ID: "bad-meta", Relative: "sessions/bad.meta.json", Data: []byte(`{`)},
		{ID: "bad-aggregate", Relative: "sessions/bad.json", Data: []byte(`{`)},
		{ID: "bad-thread", Relative: "threads/bad.json", Data: []byte(`{`)},
		{ID: "bad-handoff", Relative: "threads/handoffs/bad.json", Data: []byte(`{`)},
	}
	results, err := finalizeLegacySessions(t.Context(), conn, sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if results[source.ID].Skipped != 1 {
			t.Fatalf("audit result %q = %#v", source.ID, results[source.ID])
		}
	}
	if _, err := store.db.Exec(`DROP TABLE sessions`); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(sessionDeleteManifest{Version: 1, Keys: []string{"deleted"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalizeLegacySessions(t.Context(), conn, []sqlitestore.LegacyInput{{
		ID: "delete", Relative: "sessions/.session-delete-v1-delete.json", Data: manifest,
	}}); err == nil {
		t.Fatal("final legacy delete relation failure was hidden")
	}
}
