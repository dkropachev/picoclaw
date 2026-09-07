//nolint:govet // Independent readiness boundary assertions intentionally use narrow errors.
package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestProbeStatusesClassifiesRealSQLiteLockContention(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.db")
	createCatalogStore(t, path, `
		CREATE TABLE marker (id INTEGER PRIMARY KEY);
		PRAGMA user_version = 1;
	`)
	locker, err := sqliteprovider.OpenStore(path, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locker.SetMaxOpenConns(1)
	connection, err := locker.Conn(t.Context())
	if err != nil {
		_ = locker.Close()
		t.Fatal(err)
	}
	var journalMode string
	if err := connection.QueryRowContext(t.Context(), `PRAGMA journal_mode = DELETE`).Scan(&journalMode); err != nil {
		_ = connection.Close()
		_ = locker.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), `BEGIN EXCLUSIVE`); err != nil {
		_ = connection.Close()
		_ = locker.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = connection.ExecContext(t.Context(), `ROLLBACK`)
		_ = connection.Close()
		_ = locker.Close()
		_ = CloseProbePools(home)
	})

	statuses, err := ProbeStatuses(t.Context(), home, &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	status := statusByID(statuses, "global/auth")
	if status.Readiness != database.StoreUnavailable ||
		database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("locked auth readiness = %#v", status)
	}
}

func TestProbeStatusesRejectsRealNestedLegacyAlias(t *testing.T) {
	home := t.TempDir()
	legacyRoot := filepath.Join(home, "auth.json")
	if err := os.Mkdir(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "legacy-target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(legacyRoot, "alias.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Cleanup(func() { _ = CloseProbePools(home) })

	statuses, err := ProbeStatuses(t.Context(), home, &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	status := statusByID(statuses, "global/auth")
	if status.Readiness != database.StoreUnavailable ||
		database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("unsafe legacy readiness = %#v", status)
	}
}

func TestLegacyInputInventorySkipsRealGenerationRootsAndOrdinaryDirectories(t *testing.T) {
	root := t.TempDir()
	generation := filepath.Join(root, "state.db")
	legacy := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(generation, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := legacyInputExists([]string{generation, legacy})
	if err != nil || !found {
		t.Fatalf("generation followed by legacy input = %t, %v", found, err)
	}

	tree := filepath.Join(root, "tree")
	nested := filepath.Join(tree, "ordinary")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "state.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := legacyInputExists([]string{tree}); err != nil || found {
		t.Fatalf("ordinary database-only directory = %t, %v", found, err)
	}
}

func TestProbeStatusesPropagatesRealMalformedSeahorseViewMetadata(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	path := filepath.Join(workspace, "sessions", "seahorse.db")
	createCatalogStore(t, path, `
		CREATE TABLE conversations (id TEXT);
		CREATE VIEW messages AS
			SELECT missing_seahorse_function() AS model_name, '' AS reasoning_content;
		CREATE TABLE message_parts (id TEXT);
		CREATE TABLE summaries (id TEXT);
		CREATE TABLE summary_parents (id TEXT);
		CREATE TABLE summary_messages (id TEXT);
		CREATE TABLE context_items (id TEXT);
		CREATE TABLE summaries_fts (id TEXT);
		CREATE TABLE messages_fts (id TEXT);
		PRAGMA user_version = 1;
	`)
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: workspace, ContextManager: "seahorse",
	}}}
	statuses, err := ProbeStatuses(t.Context(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseProbePools(home) })
	status := statusByDomain(t, cfg, home, statuses, "seahorse")
	if status.Readiness != database.StoreUnavailable ||
		database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("malformed Seahorse readiness = %#v", status)
	}
}

func statusByID(statuses []database.StoreStatus, id database.StoreID) database.StoreStatus {
	for _, status := range statuses {
		if status.ID == id {
			return status
		}
	}
	return database.StoreStatus{}
}
