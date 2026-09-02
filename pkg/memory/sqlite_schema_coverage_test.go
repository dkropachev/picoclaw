package memory

import (
	"database/sql"
	"testing"
)

func TestSessionSchemaRejectsUnexpectedIncompleteAndInvalidNestedState(t *testing.T) {
	t.Run("unexpected object", func(t *testing.T) {
		store, conn := newSessionSchemaCoverageConn(t)
		if _, err := store.db.Exec(`CREATE TABLE unexpected_object (id TEXT)`); err != nil {
			t.Fatal(err)
		}
		if err := validateSessionsObjectSet(t.Context(), conn); err == nil {
			t.Fatal("unexpected schema object accepted")
		}
		if err := validateSessionsSchema(t.Context(), conn); err == nil {
			t.Fatal("full schema validation accepted unexpected object")
		}
	})
	t.Run("incomplete object set", func(t *testing.T) {
		store, conn := newSessionSchemaCoverageConn(t)
		if _, err := store.db.Exec(`DROP INDEX sessions_updated_idx`); err != nil {
			t.Fatal(err)
		}
		if err := validateSessionsObjectSet(t.Context(), conn); err == nil {
			t.Fatal("incomplete schema object set accepted")
		}
	})
	for name, raw := range map[string][]byte{
		"invalid":      []byte(`{`),
		"trailing":     []byte(`{} {}`),
		"noncanonical": []byte(`{"b":1,"a":2}`),
	} {
		t.Run(name, func(t *testing.T) {
			store, conn := newSessionSchemaCoverageConn(t)
			if err := store.AddMessage(t.Context(), "key", "user", "one"); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(),
				`UPDATE session_messages SET nested_payload = ? WHERE session_key = 'key'`, raw,
			); err != nil {
				t.Fatal(err)
			}
			if err := validateSessionsDataBounds(t.Context(), conn); err == nil {
				t.Fatal("invalid nested payload accepted")
			}
		})
	}
	t.Run("data query failure", func(t *testing.T) {
		store, conn := newSessionSchemaCoverageConn(t)
		if _, err := store.db.Exec(`DROP TABLE session_messages`); err != nil {
			t.Fatal(err)
		}
		if err := validateSessionsDataBounds(t.Context(), conn); err == nil {
			t.Fatal("missing data table accepted")
		}
	})
}

func TestSessionSchemaRejectsBrokenThreadRelationships(t *testing.T) {
	t.Run("noncontiguous", func(t *testing.T) {
		store, conn := newSessionSchemaCoverageConn(t)
		if err := store.AddMessage(t.Context(), "key", "user", "one"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(
			`UPDATE session_messages SET sequence = 2 WHERE session_key = 'key'`,
		); err != nil {
			t.Fatal(err)
		}
		if err := validateSessionsRelationships(t.Context(), conn); err == nil {
			t.Fatal("noncontiguous relationship accepted")
		}
	})
	t.Run("missing reciprocal membership", func(t *testing.T) {
		store, conn := newSessionSchemaCoverageConn(t)
		if _, err := store.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`
			INSERT INTO sessions(session_key, version) VALUES ('key', 1);
			INSERT INTO threads(
				thread_id, ui_session_id, primary_session_key, agent_id, owner_identity,
				title, thread_type, source_query, registration,
				created_seconds, created_nanos, updated_seconds, updated_nanos, version
			) VALUES ('thread', 'thread', 'key', 'main', 'owner', 'title', 'general', '',
				'manual', 0, 0, 0, 0, 1);
		`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
			t.Fatal(err)
		}
		if err := validateSessionsRelationships(t.Context(), conn); err == nil {
			t.Fatal("thread without primary membership accepted")
		}
	})
}

func newSessionSchemaCoverageConn(t *testing.T) (*SQLiteStore, *sql.Conn) {
	t.Helper()
	store := newSQLiteCoverageStore(t)
	conn, err := store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return store, conn
}
