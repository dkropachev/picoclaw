//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func privateSessionsFixture(t *testing.T) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(workspace, "sessions")
	return workspace, dir
}

func TestSQLiteStoreFreshSchemaHardeningAndReopen(t *testing.T) {
	_, dir := privateSessionsFixture(t)
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := store.DBPath()
	if path != filepath.Join(dir, SessionsDatabaseFilename) {
		t.Fatalf("DBPath() = %q", path)
	}
	var version, foreignKeys, busyTimeout, synchronous int
	var journal string
	for query, target := range map[string]any{
		"PRAGMA user_version": &version, "PRAGMA foreign_keys": &foreignKeys,
		"PRAGMA busy_timeout": &busyTimeout, "PRAGMA synchronous": &synchronous,
		"PRAGMA journal_mode": &journal,
	} {
		if err := store.SQLDB().QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 || journal != "wal" {
		t.Fatalf("pragmas = version:%d fk:%d busy:%d sync:%d journal:%q",
			version, foreignKeys, busyTimeout, synchronous, journal)
	}
	for _, path := range []string{dir, store.DBPath()} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o700)
		if !info.IsDir() {
			want = 0o600
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode %s = %o, want %o", path, info.Mode().Perm(), want)
		}
	}

	wantObjects := make([]string, 0, len(sessionsSchemaObjects)+3)
	for _, object := range sessionsSchemaObjects {
		wantObjects = append(wantObjects, object.name)
	}
	wantObjects = append(wantObjects,
		"storage_imports", "storage_import_issues", "storage_imports_archive_status_idx",
	)
	sort.Strings(wantObjects)
	schemaRows, queryErr := store.SQLDB().Query(`SELECT name FROM sqlite_schema
        WHERE type IN ('table','index','trigger','view') AND name NOT LIKE 'sqlite_%'
        ORDER BY name`)
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	defer schemaRows.Close()
	var gotObjects []string
	for schemaRows.Next() {
		var name string
		if scanErr := schemaRows.Scan(&name); scanErr != nil {
			t.Fatal(scanErr)
		}
		gotObjects = append(gotObjects, name)
	}
	if rowsErr := schemaRows.Err(); rowsErr != nil {
		t.Fatal(rowsErr)
	}
	if fmt.Sprint(gotObjects) != fmt.Sprint(wantObjects) {
		t.Fatalf("schema objects = %v, want %v", gotObjects, wantObjects)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if matches, err := filepath.Glob(filepath.Join(dir, "*.json*")); err != nil || len(matches) != 0 {
		t.Fatalf("fresh SQLite store wrote JSON: %v, %v", matches, err)
	}
}

func TestSQLiteStoreRoundTripSnapshotCASAndConcurrentAppends(t *testing.T) {
	_, dir := privateSessionsFixture(t)
	left, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()

	created := time.Date(2026, 8, 31, 12, 34, 56, 987654321, time.FixedZone("fixture", -4*3600))
	message := providers.Message{
		Role: "assistant", Content: "stored", ModelName: "model", CreatedAt: &created,
		ReasoningContent: "reason", ToolCallID: "result-1",
		Attachments: []providers.Attachment{{Type: "file", Filename: "result.json"}},
		ToolCalls: []providers.ToolCall{{
			ID: "call-1", Type: "function",
			Function: &providers.FunctionCall{Name: "inspect", Arguments: `{"n":9007199254740993}`},
		}},
	}
	if err := left.AddFullMessage(context.Background(), "session", message); err != nil {
		t.Fatal(err)
	}
	scope := json.RawMessage(
		`{"version":1,"agent_id":"main","channel":"pico","account":"default","dimensions":["chat"],"values":{"chat":"pico:one","workflow_session":"compat"}}`,
	)
	if err := left.UpsertSessionMeta(
		context.Background(), "session", scope, []string{"alias-one", "alias-two"},
	); err != nil {
		t.Fatal(err)
	}
	canonical, history, meta, found, err := right.ReadSessionSnapshot(context.Background(), "alias-one")
	if err != nil || !found || canonical != "session" || len(history) != 1 {
		t.Fatalf("snapshot = key:%q found:%v history:%#v err:%v", canonical, found, history, err)
	}
	if history[0].CreatedAt == nil || !history[0].CreatedAt.Equal(created) ||
		history[0].CreatedAt.Nanosecond() != created.Nanosecond() ||
		history[0].ToolCalls[0].Function.Arguments != `{"n":9007199254740993}` ||
		history[0].Attachments[0].Filename != "result.json" {
		t.Fatalf("round-trip message = %#v", history[0])
	}
	if meta.Revision == "" || fmt.Sprint(meta.Aliases) != "[alias-one alias-two]" {
		t.Fatalf("metadata = %#v", meta)
	}
	var decodedScope sqliteSessionScope
	if err := json.Unmarshal(meta.Scope, &decodedScope); err != nil ||
		decodedScope.Values["workflow_session"] != "compat" ||
		fmt.Sprint(decodedScope.Dimensions) != "[chat]" {
		t.Fatalf("scope extras = %#v, %v", decodedScope, err)
	}
	replacement := SessionSnapshotReplacement{
		Key: "session", History: []providers.Message{{Role: "user", Content: "replacement"}},
		Summary: "summary", Scope: scope, Aliases: meta.Aliases, ExpectedRevision: meta.Revision,
	}
	if err := left.ReplaceSessionSnapshot(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if err := right.ReplaceSessionSnapshot(context.Background(), replacement); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("stale replacement = %v", err)
	}

	const workers = 8
	const appends = 20
	start := make(chan struct{})
	var wait sync.WaitGroup
	errorsCh := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			store := left
			if worker%2 != 0 {
				store = right
			}
			for index := 0; index < appends; index++ {
				if err := store.AddMessage(
					context.Background(), "concurrent", "user", fmt.Sprintf("%d-%d", worker, index),
				); err != nil {
					errorsCh <- err
					return
				}
			}
		}(worker)
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	concurrent, err := left.GetHistory(context.Background(), "concurrent")
	if err != nil || len(concurrent) != workers*appends {
		t.Fatalf("concurrent history = %d, %v", len(concurrent), err)
	}
}

func TestSQLiteStoreAliasPromotionRebindsRelationshipsTransactionally(t *testing.T) {
	_, dir := privateSessionsFixture(t)
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const alias = "legacy-direct"
	const canonical = "canonical-direct"
	if err := store.AddMessage(context.Background(), alias, "user", "legacy history"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSessionHistory(context.Background(), canonical); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 1, 2, 3, 456789123, time.UTC)
	seconds, nanos := now.Unix(), now.Nanosecond()
	if err := store.Immediate(context.Background(), func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO threads (
            thread_id, ui_session_id, primary_session_key, agent_id, owner_identity,
            title, thread_type, source_query, registration, created_seconds,
            created_nanos, updated_seconds, updated_nanos, version
        ) VALUES ('thread', 'ui', ?, 'main', 'owner', 'title', 'general', '',
            'manual', ?, ?, ?, ?, 1)`, alias, seconds, nanos, seconds, nanos); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO thread_sessions (
            thread_id, sequence, session_key, is_primary
        ) VALUES ('thread', 0, ?, 1)`, alias); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_thread_links (
            session_key, thread_id, attached_seconds, attached_nanos
        ) VALUES (?, 'thread', ?, ?)`, alias, seconds, nanos); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO thread_handoffs (
            handoff_id, origin_session_key, target_thread_id, target_session_id,
            agent_id, summary, created_seconds, created_nanos, version
        ) VALUES ('handoff', ?, 'thread', 'ui', 'main', '', ?, ?, 1)`,
			alias, seconds, nanos)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	promoted, err := store.PromoteAliasHistory(
		context.Background(), canonical, json.RawMessage(`{}`), []string{alias},
	)
	if err != nil || !promoted {
		t.Fatalf("PromoteAliasHistory() = %v, %v", promoted, err)
	}
	for query, want := range map[string]string{
		`SELECT primary_session_key FROM threads WHERE thread_id = 'thread'`:                    canonical,
		`SELECT session_key FROM thread_sessions WHERE thread_id = 'thread' AND is_primary = 1`: canonical,
		`SELECT session_key FROM session_thread_links WHERE thread_id = 'thread'`:               canonical,
		`SELECT origin_session_key FROM thread_handoffs WHERE handoff_id = 'handoff'`:           canonical,
	} {
		var got string
		if err := store.SQLDB().QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %q, %v; want %q", query, got, err, want)
		}
	}
	var aliasRows int
	if err := store.SQLDB().QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE session_key = ?`, alias,
	).Scan(&aliasRows); err != nil || aliasRows != 0 {
		t.Fatalf("alias rows = %d, %v", aliasRows, err)
	}
	history, err := store.GetHistory(context.Background(), canonical)
	if err != nil || len(history) != 1 || history[0].Content != "legacy history" {
		t.Fatalf("promoted history = %#v, %v", history, err)
	}
}

func TestSQLiteStoreLegacyFixtureAuditArchiveAndIdempotence(t *testing.T) {
	workspace, dir := privateSessionsFixture(t)
	threadsDir := filepath.Join(workspace, "threads")
	handoffsDir := filepath.Join(threadsDir, "handoffs")
	for _, path := range []string{dir, threadsDir, handoffsDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Date(2025, 3, 4, 5, 6, 7, 123456789, time.UTC)
	scope := json.RawMessage(
		`{"version":1,"agent_id":"main","channel":"pico","account":"default","dimensions":["chat"],"values":{"chat":"pico:legacy"}}`,
	)
	meta := SessionMeta{
		Key: "legacy-session", Summary: "legacy summary", Count: 3,
		CreatedAt: created, UpdatedAt: created, Scope: scope, Aliases: []string{"legacy-alias"},
		HistorySlot: "a", ThreadID: "thread-from-meta", ThreadTitle: "Legacy thread",
		ThreadType: "coding", ThreadAttachedAt: created,
	}
	writeLegacyJSONTestFile(t, workspace, "sessions/legacy.meta.json", meta)
	writeLegacyLinesTestFile(t, workspace, "sessions/legacy.history-a", []string{
		`{"role":"user","content":"first"}`,
		`{"role":"assistant","content":"second"}`,
		`{"role":`,
	})
	writeLegacyLinesTestFile(t, workspace, "sessions/legacy.history-b", []string{
		`{"role":"user","content":"inactive-secret"}`,
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/other.json", jsonSession{
		Key: "other", Messages: []providers.Message{{Role: "user", Content: "other history"}},
		Summary: "other", Created: created, Updated: created,
	})
	writeLegacyJSONTestFile(t, workspace, "sessions/z-duplicate.json", jsonSession{
		Key: "legacy-session", Messages: []providers.Message{{Role: "user", Content: "loser"}},
		Created: created, Updated: created,
	})
	writeLegacyTestFile(t, workspace, "sessions/broken.json", []byte(`{"key":`))
	thread := legacyThreadMeta{
		ID: "thread-two", UISessionID: "ui-two", PrimarySessionKey: "other",
		Title: "Other thread", Type: "general", Context: map[string]string{"z": "last", "a": "first"},
		SessionKeys: []string{"other"}, Registration: "migrated", CreatedAt: created, UpdatedAt: created,
	}
	writeLegacyJSONTestFile(t, workspace, "threads/thread-two.json", thread)
	writeLegacyJSONTestFile(t, workspace, "threads/handoffs/handoff-one.json", legacyThreadHandoff{
		ID: "handoff-one", OriginSessionKey: "legacy-session", TargetThreadID: "thread-two",
		TargetSessionID: "ui-two", AgentID: "main", Summary: "continue", CreatedAt: created,
	})
	writeLegacyJSONTestFile(t, workspace, "threads/invalid.json", legacyThreadMeta{
		ID: strings.Repeat("x", 16_385), PrimarySessionKey: "other", CreatedAt: created, UpdatedAt: created,
	})

	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	canonical, history, gotMeta, found, err := store.ReadSessionSnapshot(
		context.Background(), "legacy-alias",
	)
	if err != nil || !found || canonical != "legacy-session" || len(history) != 2 ||
		history[0].Content != "first" || history[1].Content != "second" ||
		gotMeta.Summary != "legacy summary" || !gotMeta.CreatedAt.Equal(created) {
		t.Fatalf("legacy snapshot = key:%q found:%v history:%#v meta:%#v err:%v",
			canonical, found, history, gotMeta, err)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM threads`:              2,
		`SELECT COUNT(*) FROM thread_handoffs`:      1,
		`SELECT COUNT(*) FROM session_thread_links`: 2,
	} {
		var count int
		if err := store.SQLDB().QueryRow(query).Scan(&count); err != nil || count != want {
			t.Fatalf("%s = %d, %v; want %d", query, count, err, want)
		}
	}
	importRows, queryErr := store.SQLDB().Query(`SELECT source_relative, imported_count, skipped_count
        FROM storage_imports ORDER BY source_relative`)
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	defer importRows.Close()
	accounting := make(map[string][2]int)
	for importRows.Next() {
		var relative string
		var imported, skipped int
		if scanErr := importRows.Scan(&relative, &imported, &skipped); scanErr != nil {
			t.Fatal(scanErr)
		}
		accounting[relative] = [2]int{imported, skipped}
	}
	if rowsErr := importRows.Err(); rowsErr != nil {
		t.Fatal(rowsErr)
	}
	for relative, want := range map[string][2]int{
		"sessions/legacy.meta.json":         {1, 0},
		"sessions/legacy.history-a":         {2, 1},
		"sessions/legacy.history-b":         {0, 1},
		"sessions/other.json":               {1, 0},
		"sessions/z-duplicate.json":         {0, 1},
		"sessions/broken.json":              {0, 1},
		"threads/thread-two.json":           {1, 0},
		"threads/handoffs/handoff-one.json": {1, 0},
		"threads/invalid.json":              {0, 1},
	} {
		if accounting[relative] != want {
			t.Fatalf("accounting[%s] = %v, want %v; all=%v", relative, accounting[relative], want, accounting)
		}
	}
	issueRows, err := store.SQLDB().Query(`SELECT issue_code, length(record_digest)
        FROM storage_import_issues ORDER BY source_id, sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer issueRows.Close()
	issues := 0
	for issueRows.Next() {
		var code string
		var digestLength int
		if err := issueRows.Scan(&code, &digestLength); err != nil {
			t.Fatal(err)
		}
		if digestLength != 32 || strings.Contains(code, "secret") {
			t.Fatalf("unsafe issue = %q/%d", code, digestLength)
		}
		issues++
	}
	if err := issueRows.Err(); err != nil {
		t.Fatal(err)
	}
	if issues < 5 {
		t.Fatalf("issue count = %d", issues)
	}
	var imports int
	if err := store.SQLDB().QueryRow(`SELECT COUNT(*) FROM storage_imports`).Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	for relative := range accounting {
		source := filepath.Join(workspace, filepath.FromSlash(relative))
		if _, err := os.Stat(source); !os.IsNotExist(err) {
			t.Fatalf("legacy source retained at %s: %v", source, err)
		}
		archive := filepath.Join(workspace, "legacy-json", "sessions-v1", filepath.FromSlash(relative))
		info, err := os.Stat(archive)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("archive %s = %#v, %v", archive, info, err)
		}
	}
	reopened, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var reopenedImports int
	if err := reopened.SQLDB().QueryRow(`SELECT COUNT(*) FROM storage_imports`).Scan(&reopenedImports); err != nil ||
		reopenedImports != imports {
		t.Fatalf("reopen import count = %d, %v; want %d", reopenedImports, err, imports)
	}
}

func TestSQLiteStoreRejectsTooNewAndRelationalCorruption(t *testing.T) {
	_, dir := privateSessionsFixture(t)
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(context.Background(), "session", "user", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().Exec(`UPDATE session_messages SET sequence = 2
        WHERE session_key = 'session'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := NewSQLiteStore(dir); err == nil || reopened != nil ||
		!strings.Contains(err.Error(), "non-contiguous sequence") {
		t.Fatalf("corrupt sequence reopened as %#v, %v", reopened, err)
	}

	_, tooNewDir := privateSessionsFixture(t)
	tooNew, err := NewSQLiteStore(tooNewDir)
	if err != nil {
		t.Fatal(err)
	}
	path := tooNew.DBPath()
	if _, err := tooNew.SQLDB().Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	_ = tooNew.Close()
	if reopened, err := NewSQLiteStore(tooNewDir); err == nil || reopened != nil ||
		!strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("too-new database reopened as %#v, %v (%s)", reopened, err, path)
	}
}

func TestSQLiteStoreLegacyUnsafeSourceAbortsWithoutArchive(t *testing.T) {
	workspace, dir := privateSessionsFixture(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "unsafe.json")
	writeLegacyTestFile(t, workspace, "sessions/unsafe.json", []byte(`{"key":"unsafe"}`))
	if err := os.Chmod(workspace, 0o777); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(dir)
	if err == nil || store != nil || !strings.Contains(err.Error(), "non-writable real directory") {
		t.Fatalf("unsafe migration = %#v, %v", store, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("unsafe source changed: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(workspace, "legacy-json", "sessions-v1", "sessions", "unsafe.json"),
	); !os.IsNotExist(
		err,
	) {
		t.Fatalf("unsafe source archived: %v", err)
	}
}

func TestSQLiteStoreLegacySymlinkEnumerationAborts(t *testing.T) {
	_, dir := privateSessionsFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "session.json"), []byte(`{"key":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store, err := NewSQLiteStore(dir)
	if err == nil || store != nil || !strings.Contains(err.Error(), "unsafe legacy sessions root") {
		t.Fatalf("symlink migration = %#v, %v", store, err)
	}
	if _, err := os.Stat(filepath.Join(outside, SessionsDatabaseFilename)); !os.IsNotExist(err) {
		t.Fatalf("symlink migration created authority: %v", err)
	}
}

func writeLegacyJSONTestFile(t *testing.T, root, relative string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyTestFile(t, root, relative, data)
}

func writeLegacyLinesTestFile(t *testing.T, root, relative string, lines []string) {
	t.Helper()
	writeLegacyTestFile(t, root, relative, []byte(strings.Join(lines, "\n")+"\n"))
}

func writeLegacyTestFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
