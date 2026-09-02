//nolint:govet // Independent fault assertions intentionally use narrow error scopes.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSQLiteStorePropagatesRelationalFaultsAcrossTypedOperations(t *testing.T) {
	scope := json.RawMessage(
		`{"version":1,"agent_id":"main","channel":"pico","account":"","dimensions":[],"values":{}}`,
	)
	tests := map[string]struct {
		seed     func(*testing.T, *SQLiteStore)
		breakSQL string
		invoke   func(*SQLiteStore) error
	}{
		"add session": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				return store.AddFullMessage(t.Context(), "key", providers.Message{Role: "user", Content: "x"})
			},
		},
		"add message": {
			breakSQL: `DROP TABLE session_messages`,
			invoke: func(store *SQLiteStore) error {
				return store.AddFullMessage(t.Context(), "key", providers.Message{Role: "user", Content: "x"})
			},
		},
		"read history resolution": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.GetHistory(t.Context(), "key")
				return err
			},
		},
		"read history messages": {
			seed: func(t *testing.T, store *SQLiteStore) {
				if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
					t.Fatal(err)
				}
			},
			breakSQL: `DROP TABLE session_messages`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.GetHistory(t.Context(), "key")
				return err
			},
		},
		"read summary": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.GetSummary(t.Context(), "key")
				return err
			},
		},
		"set summary": {
			breakSQL: `DROP TABLE sessions`,
			invoke:   func(store *SQLiteStore) error { return store.SetSummary(t.Context(), "key", "summary") },
		},
		"set history": {
			breakSQL: `DROP TABLE session_messages`,
			invoke: func(store *SQLiteStore) error {
				return store.SetHistory(t.Context(), "key", []providers.Message{{Role: "user", Content: "x"}})
			},
		},
		"truncate history": {
			seed: func(t *testing.T, store *SQLiteStore) {
				if err := store.AddMessage(t.Context(), "key", "user", "x"); err != nil {
					t.Fatal(err)
				}
			},
			breakSQL: `DROP TABLE session_messages`,
			invoke:   func(store *SQLiteStore) error { return store.TruncateHistory(t.Context(), "key", 1) },
		},
		"list sessions": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				store.ListSessions()
				return store.lastListErrorForCoverage()
			},
		},
		"resolve alias": {
			breakSQL: `DROP TABLE session_aliases`,
			invoke: func(store *SQLiteStore) error {
				_, _, err := store.ResolveSessionKey(t.Context(), "missing")
				return err
			},
		},
		"read metadata": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.GetSessionMeta(t.Context(), "key")
				return err
			},
		},
		"read metadata scope": {
			seed: func(t *testing.T, store *SQLiteStore) {
				if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
					t.Fatal(err)
				}
			},
			breakSQL: `DROP TABLE session_scopes`,
			invoke:   func(store *SQLiteStore) error { _, err := store.GetSessionMeta(t.Context(), "key"); return err },
		},
		"read metadata aliases": {
			seed: func(t *testing.T, store *SQLiteStore) {
				if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
					t.Fatal(err)
				}
			},
			breakSQL: `DROP TABLE session_aliases`,
			invoke:   func(store *SQLiteStore) error { _, err := store.GetSessionMeta(t.Context(), "key"); return err },
		},
		"read metadata messages": {
			seed: func(t *testing.T, store *SQLiteStore) {
				if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
					t.Fatal(err)
				}
			},
			breakSQL: `DROP TABLE session_messages`,
			invoke:   func(store *SQLiteStore) error { _, err := store.GetSessionMeta(t.Context(), "key"); return err },
		},
		"write metadata scope": {
			breakSQL: `DROP TABLE session_scopes`,
			invoke: func(store *SQLiteStore) error {
				return store.UpsertSessionMeta(t.Context(), "key", scope, nil)
			},
		},
		"write metadata aliases": {
			breakSQL: `DROP TABLE session_aliases`,
			invoke: func(store *SQLiteStore) error {
				return store.UpsertSessionMeta(t.Context(), "key", scope, []string{"alias"})
			},
		},
		"read state messages": {
			seed: func(t *testing.T, store *SQLiteStore) {
				if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
					t.Fatal(err)
				}
			},
			breakSQL: `DROP TABLE session_messages`,
			invoke: func(store *SQLiteStore) error {
				_, _, _, _, _, err := store.ReadSessionStateStrict(t.Context(), "key")
				return err
			},
		},
		"read state facade resolution": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, _, _, err := store.ReadSessionState(t.Context(), "key")
				return err
			},
		},
		"read state facade metadata": {
			seed: func(t *testing.T, store *SQLiteStore) {
				if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
					t.Fatal(err)
				}
			},
			breakSQL: `DROP TABLE session_scopes`,
			invoke: func(store *SQLiteStore) error {
				_, _, _, err := store.ReadSessionState(t.Context(), "key")
				return err
			},
		},
		"read state facade messages": {
			seed: func(t *testing.T, store *SQLiteStore) {
				if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
					t.Fatal(err)
				}
			},
			breakSQL: `DROP TABLE session_messages`,
			invoke: func(store *SQLiteStore) error {
				_, _, _, err := store.ReadSessionState(t.Context(), "key")
				return err
			},
		},
		"replace snapshot": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				return store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{Key: "key", Scope: scope})
			},
		},
		"admit metadata": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.AdmitSessionMeta(
					t.Context(),
					"key",
					func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
						return SessionMetaAdmissionDecision{Update: true, Scope: scope}, nil
					},
				)
				return err
			},
		},
		"promote alias": {
			breakSQL: `DROP TABLE session_messages`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.PromoteAliasHistory(t.Context(), "key", scope, []string{"alias"})
				return err
			},
		},
		"strict update": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, _, err := store.UpdateSessionMetaStrict(
					t.Context(),
					"key",
					func(*SessionMeta, SessionMetaMutationState) error { return nil },
				)
				return err
			},
		},
		"compare swap": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.CompareAndSwapSessionMetaStrict(t.Context(), "key", SessionMeta{}, nil)
				return err
			},
		},
		"compare delete": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.CompareAndDeleteEmptySessionStrict(t.Context(), "key", SessionMeta{})
				return err
			},
		},
		"ensure session": {
			breakSQL: `DROP TABLE sessions`,
			invoke:   func(store *SQLiteStore) error { return store.EnsureSessionHistory(t.Context(), "key") },
		},
		"delete sessions": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store *SQLiteStore) error {
				_, err := store.DeleteSessions(t.Context(), []string{"key"})
				return err
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			if test.seed != nil {
				test.seed(t, store)
			}
			if _, err := store.db.Exec(test.breakSQL); err != nil {
				t.Fatal(err)
			}
			if err := test.invoke(store); err == nil {
				t.Fatal("relational fault was hidden")
			}
		})
	}
}

func TestSQLiteStoreTypedBoundaryVariants(t *testing.T) {
	store := newSQLiteCoverageStore(t)
	scope := json.RawMessage(
		`{"version":1,"agent_id":"main","channel":"pico","account":"","dimensions":[],"values":{}}`,
	)
	if (*SQLiteStore)(nil).StoreID() != "" {
		t.Fatal("nil store exposed a StoreID")
	}
	_ = (*SQLiteStore)(nil).ThreadStore()
	if err := store.immediate(t.Context(), nil); err == nil {
		t.Fatal("nil transaction callback accepted")
	}
	if err := store.AddFullMessage(t.Context(), "thought", providers.Message{
		Role: "assistant", ReasoningContent: "transient",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHistory(t.Context(), "history", []providers.Message{
		{Role: "assistant", ReasoningContent: "transient"},
		{Role: "user", Content: "persisted"},
	}); err != nil {
		t.Fatal(err)
	}
	if history, err := store.GetHistory(t.Context(), "history"); err != nil || len(history) != 1 ||
		history[0].CreatedAt == nil {
		t.Fatalf("filtered history = %#v, %v", history, err)
	}
	if err := store.TruncateHistory(t.Context(), "history", 0); err != nil {
		t.Fatal(err)
	}
	if history, meta, modified, err := store.ReadSessionState(
		t.Context(),
		"missing",
	); err != nil || len(history) != 0 || meta.Key != "missing" ||
		!modified.IsZero() {
		t.Fatalf("missing state = %#v %#v %v %v", history, meta, modified, err)
	}
	if history, meta, _, err := store.ReadSessionState(
		t.Context(),
		"history",
	); err != nil || len(history) != 0 ||
		meta.Key != "history" {
		t.Fatalf("existing state = %#v %#v %v", history, meta, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, _, _, err := store.ReadSessionStateStrict(canceled, "history"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled strict state error = %v", err)
	}

	if err := store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{
		Key: "snapshot", Scope: scope,
		History: []providers.Message{{Role: "user", Content: "one"}},
	}); err != nil {
		t.Fatal(err)
	}
	_, history, meta, found, err := store.ReadSessionSnapshot(t.Context(), "snapshot")
	if err != nil || !found || len(history) != 1 || meta.Revision == "" {
		t.Fatalf("snapshot = %#v %#v %t %v", history, meta, found, err)
	}
	if err := store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{
		Key: "snapshot", Scope: scope, ExpectedRevision: meta.Revision,
		History: []providers.Message{{Role: "user", Content: "two"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []SessionSnapshotReplacement{
		{Key: "snapshot", Scope: scope},
		{Key: "snapshot", Scope: scope, ExpectedRevision: "stale"},
		{Key: "missing-snapshot", Scope: scope, ExpectedRevision: "unexpected"},
	} {
		if err := store.ReplaceSessionSnapshot(t.Context(), replacement); !errors.Is(err, ErrSnapshotConflict) {
			t.Fatalf("snapshot conflict error = %v", err)
		}
	}

	if _, err := store.AdmitSessionMeta(t.Context(), " ", nil); err == nil {
		t.Fatal("invalid metadata admission accepted")
	}
	want := errors.New("decision failed")
	if changed, err := store.AdmitSessionMeta(
		t.Context(), "admit-error", func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
			return SessionMetaAdmissionDecision{}, want
		},
	); changed || !errors.Is(err, want) {
		t.Fatalf("admission callback failure = %t, %v", changed, err)
	}
	if changed, err := store.AdmitSessionMeta(
		t.Context(), "admit-noop", func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
			return SessionMetaAdmissionDecision{}, nil
		},
	); err != nil || changed {
		t.Fatalf("admission no-op = %t, %v", changed, err)
	}
	if err := store.AddMessage(t.Context(), "legacy-admit", "user", "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSessionHistory(t.Context(), "canonical-admit"); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.AdmitSessionMeta(
		t.Context(), "canonical-admit",
		func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
			return SessionMetaAdmissionDecision{
				Update: true, Scope: scope,
				Aliases:          []string{"agent:main:main", "legacy-admit"},
				ExclusiveAliases: true, PromoteAliasHistory: true,
			}, nil
		},
	); err != nil || !changed {
		t.Fatalf("promoting admission = %t, %v", changed, err)
	}
	if history, err := store.GetHistory(t.Context(), "canonical-admit"); err != nil || len(history) != 1 {
		t.Fatalf("promoted admission history = %#v, %v", history, err)
	}
	if err := store.UpsertSessionMeta(
		t.Context(), "canonical-admit", scope, []string{"requested-admit"},
	); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.AdmitSessionMeta(
		t.Context(), "requested-admit",
		func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
			return SessionMetaAdmissionDecision{
				Update: true, Scope: scope, PreserveRequestedAlias: true,
			}, nil
		},
	); err != nil || !changed {
		t.Fatalf("requested-alias admission = %t, %v", changed, err)
	}

	for name, update := range map[string]func(*SessionMeta, SessionMetaMutationState) error{
		"callback": func(*SessionMeta, SessionMetaMutationState) error { return want },
		"key":      func(meta *SessionMeta, _ SessionMetaMutationState) error { meta.Key = "changed"; return nil },
		"history":  func(meta *SessionMeta, _ SessionMetaMutationState) error { meta.Count++; return nil },
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := store.UpdateSessionMetaStrict(t.Context(), "snapshot", update); err == nil {
				t.Fatal("invalid strict metadata update succeeded")
			}
		})
	}
	if _, _, err := store.UpdateSessionMetaStrict(t.Context(), " ", nil); err == nil {
		t.Fatal("invalid strict metadata input accepted")
	}
	if changed, err := store.CompareAndSwapSessionMetaStrict(
		t.Context(), "missing", SessionMeta{}, nil,
	); err != nil || changed {
		t.Fatalf("missing CAS = %t, %v", changed, err)
	}
	current, err := store.GetSessionMeta(t.Context(), "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	stale := cloneSessionMeta(current)
	stale.Summary = "stale"
	if changed, err := store.CompareAndSwapSessionMetaStrict(
		t.Context(), "snapshot", stale, nil,
	); err != nil || changed {
		t.Fatalf("stale CAS = %t, %v", changed, err)
	}
	if changed, err := store.CompareAndSwapSessionMetaStrict(
		t.Context(), "snapshot", current, nil,
	); err == nil || changed {
		t.Fatalf("nonempty CAS delete = %t, %v", changed, err)
	}
	if changed, err := store.CompareAndDeleteEmptySessionStrict(
		t.Context(), "snapshot", current,
	); err == nil || changed {
		t.Fatalf("nonempty compare-delete = %t, %v", changed, err)
	}
	if _, err := store.DeleteSessions(t.Context(), nil); err == nil {
		t.Fatal("empty grouped delete accepted")
	}
	if (*SQLiteStore)(nil).ListSessions() != nil {
		t.Fatal("nil store listed sessions")
	}
}

func TestSQLiteSessionHelpersPropagateClosedConnectionFailures(t *testing.T) {
	_, conn := newSessionSchemaCoverageConn(t)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	message := providers.Message{Role: "user", Content: "one", CreatedAt: &now}
	tests := map[string]func() error{
		"resolve":              func() error { _, _, err := resolveSessionKeyConn(t.Context(), conn, "key", true); return err },
		"ensure":               func() error { return ensureSessionConn(t.Context(), conn, "key", now) },
		"version":              func() error { _, err := sessionVersionConn(t.Context(), conn, "key"); return err },
		"bump":                 func() error { return bumpSessionVersionConn(t.Context(), conn, "key", 1, now) },
		"insert message":       func() error { return insertMessageConn(t.Context(), conn, "key", 0, message) },
		"read messages":        func() error { _, err := readMessagesConn(t.Context(), conn, "key"); return err },
		"read scope":           func() error { _, err := readScopeConn(t.Context(), conn, "key"); return err },
		"read aliases":         func() error { _, err := readAliasesConn(t.Context(), conn, "key"); return err },
		"read metadata":        func() error { _, _, err := readSessionMetaConn(t.Context(), conn, "key"); return err },
		"write scope":          func() error { return writeScopeConn(t.Context(), conn, "key", nil) },
		"validate alias":       func() error { return validateAliasWriteConn(t.Context(), conn, "key", []string{"alias"}, true) },
		"write aliases":        func() error { return writeAliasesConn(t.Context(), conn, "key", []string{"alias"}, true) },
		"write metadata":       func() error { return writeSessionMetaConn(t.Context(), conn, "key", SessionMeta{Key: "key"}, true) },
		"promote alias":        func() error { _, err := promoteAliasConn(t.Context(), conn, "key", "alias"); return err },
		"rebind relationships": func() error { return rebindPromotedSessionRelationshipsConn(t.Context(), conn, "alias", "key") },
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := test(); err == nil {
				t.Fatal("closed connection failure was hidden")
			}
		})
	}
}

func TestSQLiteSessionScopeAliasAndPromotionBoundaries(t *testing.T) {
	validScope := json.RawMessage(
		`{"version":1,"agent_id":"main","channel":"pico","account":"","dimensions":["sender"],"values":{"sender":"user","extra":"value"}}`,
	)
	for name, scope := range map[string]json.RawMessage{
		"invalid JSON": json.RawMessage(`{`),
		"missing dimension": json.RawMessage(
			`{"version":1,"dimensions":["sender"],"values":{}}`,
		),
		"duplicate dimension": json.RawMessage(
			`{"version":1,"dimensions":["sender","sender"],"values":{"sender":"user"}}`,
		),
	} {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			if err := store.UpsertSessionMeta(t.Context(), "key", scope, nil); err == nil {
				t.Fatal("invalid session scope accepted")
			}
		})
	}
	store := newSQLiteCoverageStore(t)
	if err := store.UpsertSessionMeta(t.Context(), "key", validScope, []string{"alias"}); err != nil {
		t.Fatal(err)
	}
	if meta, err := store.GetSessionMeta(t.Context(), "key"); err != nil || len(meta.Aliases) != 1 {
		t.Fatalf("valid scoped metadata = %#v, %v", meta, err)
	}
	if err := store.immediate(t.Context(), func(ctx context.Context, conn *sql.Conn) error {
		if err := writeAliasesConn(ctx, conn, "key", []string{" alias "}, false); err == nil {
			t.Fatal("noncanonical aliases accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store = newSQLiteCoverageStore(t)
	if err := store.EnsureSessionHistory(t.Context(), "canonical"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSessionHistory(t.Context(), "direct"); err != nil {
		t.Fatal(err)
	}
	if err := store.immediate(t.Context(), func(ctx context.Context, conn *sql.Conn) error {
		return writeAliasesConn(ctx, conn, "canonical", []string{"direct"}, true)
	}); err == nil {
		t.Fatal("direct session accepted as exclusive alias")
	}
	if err := store.UpsertSessionMeta(t.Context(), "owner", nil, []string{"taken"}); err != nil {
		t.Fatal(err)
	}
	if err := store.immediate(t.Context(), func(ctx context.Context, conn *sql.Conn) error {
		return writeAliasesConn(ctx, conn, "canonical", []string{"taken"}, true)
	}); err == nil {
		t.Fatal("owned alias accepted exclusively")
	}

	for name, setup := range map[string]func(*testing.T, *SQLiteStore){
		"canonical nonempty": func(t *testing.T, store *SQLiteStore) {
			if err := store.SetSummary(t.Context(), "canonical", "keep"); err != nil {
				t.Fatal(err)
			}
			if err := store.AddMessage(t.Context(), "legacy", "user", "legacy"); err != nil {
				t.Fatal(err)
			}
		},
		"alias missing": func(t *testing.T, store *SQLiteStore) {
			if err := store.EnsureSessionHistory(t.Context(), "canonical"); err != nil {
				t.Fatal(err)
			}
		},
		"alias empty": func(t *testing.T, store *SQLiteStore) {
			if err := store.EnsureSessionHistory(t.Context(), "canonical"); err != nil {
				t.Fatal(err)
			}
			if err := store.EnsureSessionHistory(t.Context(), "legacy"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			setup(t, store)
			promoted, err := store.PromoteAliasHistory(
				t.Context(), "canonical", validScope, []string{"legacy"},
			)
			if err != nil || promoted {
				t.Fatalf("nonpromoting alias boundary = %t, %v", promoted, err)
			}
		})
	}
	store = newSQLiteCoverageStore(t)
	if err := store.EnsureSessionHistory(t.Context(), "canonical"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(t.Context(), "legacy", "user", "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA foreign_keys = OFF; DROP TABLE thread_sessions;`); err != nil {
		t.Fatal(err)
	}
	if promoted, err := store.PromoteAliasHistory(
		t.Context(), "canonical", validScope, []string{"legacy"},
	); err == nil || promoted {
		t.Fatalf("relationship rebind fault = %t, %v", promoted, err)
	}
}

func TestSQLiteSerializationAndTimeBoundaries(t *testing.T) {
	if contextOrBackground(nil) == nil {
		t.Fatal("nil context did not receive background context")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&SQLiteStore{}).Compact(canceled, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled local compact error = %v", err)
	}
	if _, _, _, err := (*SQLiteStore)(nil).ReadSessionState(t.Context(), "key"); err == nil {
		t.Fatal("nil store state read succeeded")
	}
	if _, err := openLocalSQLiteStore(t.Context(), " "); err == nil {
		t.Fatal("blank local store path accepted")
	}
	if got := timeFromSQLite(sql.NullInt64{}, sql.NullInt64{Int64: 1, Valid: true}); !got.IsZero() {
		t.Fatalf("partial SQLite time = %v", got)
	}
	if got := timeFromSQLite(
		sql.NullInt64{Int64: 1, Valid: true}, sql.NullInt64{Int64: 2, Valid: true},
	); !got.Equal(time.Unix(1, 2).UTC()) {
		t.Fatalf("SQLite time = %v", got)
	}
	for _, payload := range [][]byte{nil, []byte(`{`), []byte(`{} {}`)} {
		if _, err := canonicalJSONBlob(payload); err == nil {
			t.Fatalf("invalid canonical JSON accepted: %q", payload)
		}
	}
	canonical, err := canonicalJSONBlob([]byte(`{"b":2,"a":1}`))
	if err != nil || string(canonical) != `{"a":1,"b":2}` {
		t.Fatalf("canonical JSON = %s, %v", canonical, err)
	}
	if payload, err := encodeStoredMessage(providers.Message{}); err != nil || payload != nil {
		t.Fatalf("empty nested payload = %s, %v", payload, err)
	}
	if _, err := encodeStoredMessage(providers.Message{
		Media: []string{strings.Repeat("x", maxLineSize)},
	}); err == nil {
		t.Fatal("oversized nested payload accepted")
	}
	var message providers.Message
	if err := decodeStoredMessageNested(nil, &message); err != nil {
		t.Fatal(err)
	}
	if err := decodeStoredMessageNested([]byte(`{`), &message); err == nil {
		t.Fatal("invalid stored nested payload accepted")
	}
	payload, err := encodeStoredMessage(providers.Message{
		Media: []string{"media"}, Parts: []providers.PromptPart{{Type: "text", Text: "part"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeStoredMessageNested(payload, &message); err != nil || len(message.Media) != 1 {
		t.Fatalf("nested message round trip = %#v, %v", message, err)
	}
}

func TestSQLiteAliasPromotionPropagatesRelationshipFaults(t *testing.T) {
	for name, relation := range map[string]string{
		"thread context":  "thread_context",
		"thread sessions": "thread_sessions",
		"session links":   "session_thread_links",
		"handoffs":        "thread_handoffs",
	} {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			seedPromotionRelationships(t, store, "legacy", "canonical")
			if _, err := store.db.Exec(`PRAGMA foreign_keys = OFF; DROP TABLE ` + relation); err != nil {
				t.Fatal(err)
			}
			if promoted, err := store.PromoteAliasHistory(
				t.Context(), "canonical", json.RawMessage(`{}`), []string{"legacy"},
			); err == nil {
				t.Fatalf("missing %s relation = promoted:%t err:%v", relation, promoted, err)
			}
		})
	}
}

func TestSQLiteStorePropagatesVersionFenceFaults(t *testing.T) {
	scope := json.RawMessage(`{"version":1,"channel":"pico","values":{}}`)
	tests := map[string]struct {
		seed   func(*testing.T, *SQLiteStore)
		invoke func(*SQLiteStore) error
	}{
		"add": {nil, func(store *SQLiteStore) error {
			return store.AddMessage(t.Context(), "key", "user", "one")
		}},
		"summary": {nil, func(store *SQLiteStore) error {
			return store.SetSummary(t.Context(), "key", "summary")
		}},
		"history": {nil, func(store *SQLiteStore) error {
			return store.SetHistory(t.Context(), "key", []providers.Message{{Role: "user", Content: "one"}})
		}},
		"truncate": {func(t *testing.T, store *SQLiteStore) {
			if err := store.AddMessage(t.Context(), "key", "user", "one"); err != nil {
				t.Fatal(err)
			}
		}, func(store *SQLiteStore) error { return store.TruncateHistory(t.Context(), "key", 0) }},
		"metadata": {nil, func(store *SQLiteStore) error {
			return store.UpsertSessionMeta(t.Context(), "key", scope, nil)
		}},
		"snapshot": {nil, func(store *SQLiteStore) error {
			return store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{Key: "key", Scope: scope})
		}},
		"admission": {nil, func(store *SQLiteStore) error {
			_, err := store.AdmitSessionMeta(
				t.Context(),
				"key",
				func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
					return SessionMetaAdmissionDecision{Update: true, Scope: scope}, nil
				},
			)
			return err
		}},
		"strict update": {func(t *testing.T, store *SQLiteStore) {
			if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
				t.Fatal(err)
			}
		}, func(store *SQLiteStore) error {
			_, _, err := store.UpdateSessionMetaStrict(
				t.Context(),
				"key",
				func(meta *SessionMeta, _ SessionMetaMutationState) error {
					meta.Summary = "updated"
					return nil
				},
			)
			return err
		}},
		"promotion": {func(t *testing.T, store *SQLiteStore) {
			if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
				t.Fatal(err)
			}
			if err := store.AddMessage(t.Context(), "legacy", "user", "one"); err != nil {
				t.Fatal(err)
			}
		}, func(store *SQLiteStore) error {
			_, err := store.PromoteAliasHistory(t.Context(), "key", scope, []string{"legacy"})
			return err
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			if test.seed != nil {
				test.seed(t, store)
			}
			if _, err := store.db.Exec(`CREATE TRIGGER fail_session_version
				BEFORE UPDATE OF version ON sessions
				BEGIN SELECT RAISE(ABORT, 'version fence'); END`); err != nil {
				t.Fatal(err)
			}
			if err := test.invoke(store); err == nil {
				t.Fatal("version fence failure was hidden")
			}
		})
	}
}

func TestSQLiteTruncatePropagatesSequenceRewriteFaults(t *testing.T) {
	for name, trigger := range map[string]string{
		"temporary sequence": `WHEN NEW.sequence >= 1000000000`,
		"final sequence":     `WHEN OLD.sequence >= 1000000000 AND NEW.sequence < 1000000000`,
	} {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			for _, content := range []string{"one", "two", "three"} {
				if err := store.AddMessage(t.Context(), "key", "user", content); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.db.Exec(`CREATE TRIGGER fail_sequence
				BEFORE UPDATE OF sequence ON session_messages ` + trigger + `
				BEGIN SELECT RAISE(ABORT, 'sequence fence'); END`); err != nil {
				t.Fatal(err)
			}
			if err := store.TruncateHistory(t.Context(), "key", 2); err == nil {
				t.Fatal("sequence rewrite fault was hidden")
			}
		})
	}
}

func TestSQLiteStorePropagatesMutationTriggerFaults(t *testing.T) {
	scope := json.RawMessage(`{"version":1,"channel":"pico","values":{}}`)
	t.Run("set history delete", func(t *testing.T) {
		store := newSQLiteCoverageStore(t)
		if err := store.AddMessage(t.Context(), "key", "user", "one"); err != nil {
			t.Fatal(err)
		}
		installMutationFailureTrigger(t, store, "session_messages", "DELETE")
		if err := store.SetHistory(t.Context(), "key", nil); err == nil {
			t.Fatal("history delete trigger was hidden")
		}
	})
	t.Run("snapshot delete", func(t *testing.T) {
		store := newSQLiteCoverageStore(t)
		if err := store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{
			Key: "key", Scope: scope, History: []providers.Message{{Role: "user", Content: "one"}},
		}); err != nil {
			t.Fatal(err)
		}
		_, _, meta, found, err := store.ReadSessionSnapshot(t.Context(), "key")
		if err != nil || !found {
			t.Fatal(err)
		}
		installMutationFailureTrigger(t, store, "session_messages", "DELETE")
		if err := store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{
			Key: "key", Scope: scope, ExpectedRevision: meta.Revision,
		}); err == nil {
			t.Fatal("snapshot delete trigger was hidden")
		}
	})
	for name, invoke := range map[string]func(*testing.T, *SQLiteStore, SessionMeta) error{
		"CAS delete": func(t *testing.T, store *SQLiteStore, meta SessionMeta) error {
			_, err := store.CompareAndSwapSessionMetaStrict(t.Context(), "key", meta, nil)
			return err
		},
		"compare delete": func(t *testing.T, store *SQLiteStore, meta SessionMeta) error {
			_, err := store.CompareAndDeleteEmptySessionStrict(t.Context(), "key", meta)
			return err
		},
		"grouped delete": func(t *testing.T, store *SQLiteStore, _ SessionMeta) error {
			_, err := store.DeleteSessions(t.Context(), []string{"key"})
			return err
		},
		"matching delete": func(t *testing.T, store *SQLiteStore, _ SessionMeta) error {
			_, err := store.DeleteSessionsWithAliasesMatching(
				t.Context(), []string{"key"}, func(SessionMeta, bool) bool { return true }, nil,
			)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
				t.Fatal(err)
			}
			meta, err := store.GetSessionMeta(t.Context(), "key")
			if err != nil {
				t.Fatal(err)
			}
			installMutationFailureTrigger(t, store, "sessions", "DELETE")
			if err := invoke(t, store, meta); err == nil {
				t.Fatal("session delete trigger was hidden")
			}
		})
	}
	for name, invoke := range map[string]func(*SQLiteStore) error{
		"metadata alias": func(store *SQLiteStore) error {
			return store.UpsertSessionMeta(t.Context(), "key", scope, []string{"alias"})
		},
		"admission alias": func(store *SQLiteStore) error {
			_, err := store.AdmitSessionMeta(t.Context(), "key", func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
				return SessionMetaAdmissionDecision{Update: true, Scope: scope, Aliases: []string{"alias"}}, nil
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			installMutationFailureTrigger(t, store, "session_aliases", "INSERT")
			if err := invoke(store); err == nil {
				t.Fatal("alias insert trigger was hidden")
			}
		})
	}
}

func installMutationFailureTrigger(t *testing.T, store *SQLiteStore, table, operation string) {
	t.Helper()
	if _, err := store.db.Exec(`CREATE TRIGGER fail_mutation BEFORE ` + operation + ` ON ` + table + `
		BEGIN SELECT RAISE(ABORT, 'mutation fence'); END`); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteRemainingMetadataBranches(t *testing.T) {
	store := newSQLiteCoverageStore(t)
	if err := store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{}); err == nil {
		t.Fatal("invalid replacement snapshot accepted")
	}
	if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
		t.Fatal(err)
	}
	if err := store.immediate(t.Context(), func(ctx context.Context, conn *sql.Conn) error {
		if err := writeSessionMetaConn(ctx, conn, "key", SessionMeta{Key: "other"}, false); err == nil {
			t.Fatal("mismatched metadata key accepted")
		}
		if err := validateAliasWriteConn(ctx, conn, "key", []string{"key"}, false); err != nil {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetSessionMeta(t.Context(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := store.CompareAndSwapSessionMetaStrict(
		t.Context(), "key", current, &SessionMeta{},
	); err != nil || !changed {
		t.Fatalf("blank-key CAS replacement = %t, %v", changed, err)
	}
	if promoted, err := store.PromoteAliasHistory(
		t.Context(), "key", nil, []string{"agent:main:main"},
	); err != nil || promoted {
		t.Fatalf("main-alias promotion = %t, %v", promoted, err)
	}
	if deleted, err := store.DeleteSessions(t.Context(), []string{"missing", "missing"}); err != nil || deleted {
		t.Fatalf("missing grouped delete = %t, %v", deleted, err)
	}
	if changed, err := store.CompareAndDeleteEmptySessionStrict(
		t.Context(), "missing", SessionMeta{},
	); err != nil || changed {
		t.Fatalf("missing compare-delete = %t, %v", changed, err)
	}
}

func TestSQLiteScopeWritePropagatesInsertFaults(t *testing.T) {
	scopes := map[string]struct {
		trigger string
		scope   json.RawMessage
	}{
		"scope row": {
			`BEFORE INSERT ON session_scopes`,
			json.RawMessage(`{"version":1,"channel":"pico","values":{}}`),
		},
		"dimension row": {
			`BEFORE INSERT ON session_scope_dimensions WHEN NEW.is_dimension = 1`,
			json.RawMessage(
				`{"version":1,"channel":"pico","dimensions":["sender"],"values":{"sender":"user"}}`,
			),
		},
		"extra row": {
			`BEFORE INSERT ON session_scope_dimensions WHEN NEW.is_dimension = 0`,
			json.RawMessage(`{"version":1,"channel":"pico","values":{"extra":"value"}}`),
		},
	}
	for name, test := range scopes {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			if _, err := store.db.Exec(`CREATE TRIGGER fail_scope ` + test.trigger + `
				BEGIN SELECT RAISE(ABORT, 'scope fence'); END`); err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertSessionMeta(t.Context(), "key", test.scope, nil); err == nil {
				t.Fatal("scope insert trigger was hidden")
			}
		})
	}
}

func TestSQLiteMetadataOperationsPropagateWriteFence(t *testing.T) {
	scope := json.RawMessage(`{"version":1,"channel":"pico","values":{}}`)
	for name, invoke := range map[string]func(*testing.T, *SQLiteStore, SessionMeta) error{
		"upsert": func(t *testing.T, store *SQLiteStore, _ SessionMeta) error {
			return store.UpsertSessionMeta(t.Context(), "key", scope, nil)
		},
		"snapshot": func(t *testing.T, store *SQLiteStore, meta SessionMeta) error {
			return store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{
				Key: "key", Scope: scope, ExpectedRevision: meta.Revision,
			})
		},
		"strict update": func(t *testing.T, store *SQLiteStore, _ SessionMeta) error {
			_, _, err := store.UpdateSessionMetaStrict(
				t.Context(), "key", func(meta *SessionMeta, _ SessionMetaMutationState) error {
					meta.Summary = "updated"
					return nil
				},
			)
			return err
		},
		"compare swap": func(t *testing.T, store *SQLiteStore, meta SessionMeta) error {
			replacement := cloneSessionMeta(meta)
			replacement.Summary = "updated"
			_, err := store.CompareAndSwapSessionMetaStrict(t.Context(), "key", meta, &replacement)
			return err
		},
		"admission": func(t *testing.T, store *SQLiteStore, _ SessionMeta) error {
			_, err := store.AdmitSessionMeta(
				t.Context(), "key", func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
					return SessionMetaAdmissionDecision{Update: true, Scope: scope}, nil
				},
			)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteCoverageStore(t)
			if err := store.EnsureSessionHistory(t.Context(), "key"); err != nil {
				t.Fatal(err)
			}
			_, _, meta, found, err := store.ReadSessionSnapshot(t.Context(), "key")
			if err != nil || !found {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`CREATE TRIGGER fail_metadata_write
				BEFORE UPDATE OF summary ON sessions
				BEGIN SELECT RAISE(ABORT, 'metadata fence'); END`); err != nil {
				t.Fatal(err)
			}
			if err := invoke(t, store, meta); err == nil {
				t.Fatal("metadata write fence was hidden")
			}
		})
	}
}

func seedPromotionRelationships(t *testing.T, store *SQLiteStore, alias, canonical string) {
	t.Helper()
	if err := store.AddMessage(t.Context(), alias, "user", "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSessionHistory(t.Context(), canonical); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seconds, nanos := now.Unix(), now.Nanosecond()
	if err := store.immediate(t.Context(), func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO threads (
			thread_id, ui_session_id, primary_session_key, agent_id, owner_identity,
			title, thread_type, source_query, registration, created_seconds,
			created_nanos, updated_seconds, updated_nanos, version
		) VALUES ('thread', 'ui', ?, 'main', 'owner', 'title', 'general', '',
			'manual', ?, ?, ?, ?, 1)`, alias, seconds, nanos, seconds, nanos); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO thread_context(thread_id, key, value)
			VALUES ('thread', 'repo', 'owner/repo')`); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO thread_aliases(thread_id, sequence, alias)
			VALUES ('thread', 0, 'thread-alias')`); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO thread_sessions(
			thread_id, sequence, session_key, is_primary) VALUES ('thread', 0, ?, 1)`, alias); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO session_thread_links(
			session_key, thread_id, attached_seconds, attached_nanos)
			VALUES (?, 'thread', ?, ?)`, alias, seconds, nanos); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO thread_handoffs(
			handoff_id, origin_session_key, target_thread_id, target_session_id,
			agent_id, summary, created_seconds, created_nanos, version)
			VALUES ('handoff', ?, 'thread', 'ui', 'main', '', ?, ?, 1)`, alias, seconds, nanos)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func newSQLiteCoverageStore(t *testing.T) *SQLiteStore {
	t.Helper()
	_, dir := privateSessionsFixture(t)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// lastListErrorForCoverage repeats ListSessions' typed query so a relational
// fault remains assertable despite the compatibility method's slice-only API.
func (s *SQLiteStore) lastListErrorForCoverage() error {
	if s == nil || s.db == nil {
		return context.Canceled
	}
	rows, err := s.db.Query(`SELECT session_key FROM sessions`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}
