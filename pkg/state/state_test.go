package state

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestSQLiteManagerCreatesPersistsAndReopensRuntimeState(t *testing.T) {
	workspace := newRuntimeWorkspace(t)
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatalf("NewSQLiteManager() error = %v", err)
	}
	if manager.GetLastChannel() != "" || manager.GetLastChatID() != "" ||
		!manager.GetTimestamp().IsZero() {
		t.Fatalf("new runtime state = %#v", manager.snapshot())
	}

	channelTime := time.Date(2026, time.August, 31, 13, 14, 15, 123456789, time.UTC)
	manager.now = func() time.Time { return channelTime }
	if err := manager.SetLastChannel("telegram:user-1"); err != nil {
		t.Fatalf("SetLastChannel() error = %v", err)
	}
	chatTime := channelTime.Add(time.Second)
	manager.now = func() time.Time { return chatTime }
	if err := manager.SetLastChatID("chat-1"); err != nil {
		t.Fatalf("SetLastChatID() error = %v", err)
	}
	if manager.GetLastChannel() != "telegram:user-1" || manager.GetLastChatID() != "chat-1" ||
		!manager.GetTimestamp().Equal(chatTime) {
		t.Fatalf("updated runtime state = %#v", manager.snapshot())
	}

	reopened, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if reopened.GetLastChannel() != "telegram:user-1" || reopened.GetLastChatID() != "chat-1" ||
		!reopened.GetTimestamp().Equal(chatTime) {
		t.Fatalf("reopened runtime state = %#v", reopened.snapshot())
	}
	if _, err := os.Stat(filepath.Join(workspace, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("new store created root JSON state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "state", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("new store created nested JSON state: %v", err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestRuntimeSQLiteSchemaPragmasConstraintsAndPermissions(t *testing.T) {
	workspace := newRuntimeWorkspace(t)
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetLastChannel("pico:user"); err != nil {
		t.Fatal(err)
	}
	configured, unlock, err := manager.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var version, foreignKeys, busyTimeout, synchronous int
	var journal string
	for _, query := range []struct {
		statement string
		dest      any
	}{
		{statement: "PRAGMA user_version", dest: &version},
		{statement: "PRAGMA foreign_keys", dest: &foreignKeys},
		{statement: "PRAGMA busy_timeout", dest: &busyTimeout},
		{statement: "PRAGMA synchronous", dest: &synchronous},
		{statement: "PRAGMA journal_mode", dest: &journal},
	} {
		if err := configured.QueryRow(query.statement).Scan(query.dest); err != nil {
			t.Fatalf("%s error = %v", query.statement, err)
		}
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journal, "wal") {
		t.Fatalf(
			"SQLite configuration = version:%d fk:%d busy:%d sync:%d journal:%q",
			version,
			foreignKeys,
			busyTimeout,
			synchronous,
			journal,
		)
	}
	conn, err := configured.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeSchema(t.Context(), conn); err != nil {
		t.Fatalf("validateRuntimeSchema() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := configured.Close(); err != nil {
		t.Fatal(err)
	}
	unlock()
	db := openRawRuntimeDatabase(t, manager.databasePath)
	defer db.Close()

	for name, statement := range map[string]string{
		"second row": `INSERT INTO runtime_state(id) VALUES (2)`,
		"partial timestamp": `UPDATE runtime_state
            SET updated_at_unix_seconds = 1, updated_at_nanosecond = NULL WHERE id = 1`,
		"bad nanosecond": `UPDATE runtime_state
            SET updated_at_unix_seconds = 1, updated_at_nanosecond = 1000000000 WHERE id = 1`,
		"bad origin":   `UPDATE runtime_state SET origin_priority = 4 WHERE id = 1`,
		"zero version": `UPDATE runtime_state SET version = 0 WHERE id = 1`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Exec(statement); err == nil {
				t.Fatalf("constraint accepted %s", name)
			}
		})
	}

	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{
			manager.databasePath:                                    0o600,
			filepath.Dir(manager.databasePath):                      0o700,
			runtimeDatabaseLockDirectory(manager.databasePath):      0o700,
			runtimeDatabaseLockPath(manager.databasePath) + ".lock": 0o600,
		} {
			info, statErr := os.Stat(path)
			if statErr != nil || info.Mode().Perm() != mode {
				t.Fatalf("mode for %s = %v, %v; want %o", path, info, statErr, mode)
			}
		}
		for _, companion := range []string{manager.databasePath + "-wal", manager.databasePath + "-shm"} {
			if info, statErr := os.Stat(companion); statErr == nil && info.Mode().Perm() != 0o600 {
				t.Fatalf("companion %s mode = %o", companion, info.Mode().Perm())
			} else if statErr != nil && !os.IsNotExist(statErr) {
				t.Fatal(statErr)
			}
		}
	}
}

func TestIndependentManagersPreserveFieldUpdatesAndObserveAuthority(t *testing.T) {
	workspace := newRuntimeWorkspace(t)
	first, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetLastChannel("discord:user"); err != nil {
		t.Fatal(err)
	}
	if err := second.SetLastChatID("chat-from-second"); err != nil {
		t.Fatal(err)
	}
	if first.GetLastChannel() != "discord:user" || first.GetLastChatID() != "chat-from-second" {
		t.Fatalf("first manager observed state = %#v", first.snapshot())
	}
	if second.GetLastChannel() != "discord:user" || second.GetLastChatID() != "chat-from-second" {
		t.Fatalf("second manager observed state = %#v", second.snapshot())
	}

	const workers = 20
	start := make(chan struct{})
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := range workers {
		go func() {
			defer wait.Done()
			<-start
			manager := first
			if index%2 != 0 {
				manager = second
			}
			if index%2 == 0 {
				errorsChannel <- manager.SetLastChannel(fmt.Sprintf("channel-%d", index))
				return
			}
			errorsChannel <- manager.SetLastChatID(fmt.Sprintf("chat-%d", index))
		}()
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent update error = %v", err)
		}
	}
	if first.GetLastChannel() == "" || first.GetLastChatID() == "" {
		t.Fatalf("concurrent state = %#v", first.snapshot())
	}
}

func TestRuntimeStateUpdatesSerializeAcrossProcesses(t *testing.T) {
	if os.Getenv("PICOCLAW_RUNTIME_STATE_HELPER") == "1" {
		workspace := os.Getenv("PICOCLAW_RUNTIME_STATE_WORKSPACE")
		manager, err := NewSQLiteManager(workspace)
		if err != nil {
			t.Fatal(err)
		}
		field := os.Getenv("PICOCLAW_RUNTIME_STATE_FIELD")
		if field == "channel" {
			err = manager.SetLastChannel("process-channel")
		} else {
			err = manager.SetLastChatID("process-chat")
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	workspace := newRuntimeWorkspace(t)
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 0, 2)
	outputs := make([]bytes.Buffer, 2)
	for index, field := range []string{"channel", "chat"} {
		command := exec.Command(os.Args[0], "-test.run=^TestRuntimeStateUpdatesSerializeAcrossProcesses$")
		command.Env = append(
			os.Environ(),
			"PICOCLAW_RUNTIME_STATE_HELPER=1",
			"PICOCLAW_RUNTIME_STATE_WORKSPACE="+workspace,
			"PICOCLAW_RUNTIME_STATE_FIELD="+field,
		)
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper %d failed: %v\n%s", index, err, outputs[index].String())
		}
	}
	if manager.GetLastChannel() != "process-channel" || manager.GetLastChatID() != "process-chat" {
		t.Fatalf("cross-process state = %#v", manager.snapshot())
	}
	db := openRawRuntimeDatabase(t, manager.databasePath)
	defer db.Close()
	var version int
	if err := db.QueryRow(`SELECT version FROM runtime_state WHERE id = 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("cross-process row version = %d, want 3", version)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestLegacyRuntimeStateMigrationPrefersNestedSourceAndArchivesBoth(t *testing.T) {
	workspace := newRuntimeWorkspace(t)
	rootState := State{
		LastChannel: "old-channel:user",
		LastChatID:  "old-chat",
		Timestamp:   time.Date(2026, time.August, 30, 10, 0, 0, 0, time.UTC),
	}
	nestedState := State{
		LastChannel: "new-channel:user",
		LastChatID:  "new-chat",
		Timestamp:   time.Date(2026, time.August, 31, 10, 0, 0, 123, time.UTC),
	}
	rootData := writeLegacyRuntimeState(t, workspace, "state.json", rootState, 0o600)
	nestedData := writeLegacyRuntimeState(t, workspace, "state/state.json", nestedState, 0o640)
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatalf("NewSQLiteManager() migration error = %v", err)
	}
	if got := manager.snapshot(); !sameRuntimeState(got, nestedState) {
		t.Fatalf("migrated state = %#v, want nested %#v", got, nestedState)
	}
	for relative, want := range map[string][]byte{
		"state.json":       rootData,
		"state/state.json": nestedData,
	} {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("legacy source %s still exists: %v", relative, err)
		}
		archive := filepath.Join(
			workspace,
			"state",
			"legacy-json",
			runtimeLegacyArchiveLabel,
			filepath.FromSlash(relative),
		)
		if data, err := os.ReadFile(archive); err != nil || !bytes.Equal(data, want) {
			t.Fatalf("archive %s = %q, %v", relative, data, err)
		}
	}
	db := openRawRuntimeDatabase(t, manager.databasePath)
	defer db.Close()
	var imports, imported, skipped, issues int
	if err := db.QueryRow(`SELECT COUNT(*), SUM(imported_count), SUM(skipped_count)
        FROM storage_imports WHERE component = ?`, runtimeDatabaseComponent).Scan(
		&imports,
		&imported,
		&skipped,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM storage_import_issues
        WHERE component = ? AND issue_code = 'source-conflict'`, runtimeDatabaseComponent).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if imports != 2 || imported != 2 || skipped != 1 || issues != 1 {
		t.Fatalf(
			"migration audit = sources:%d imported:%d skipped:%d conflicts:%d",
			imports,
			imported,
			skipped,
			issues,
		)
	}
	reopened, err := NewSQLiteManager(workspace)
	if err != nil || !sameRuntimeState(reopened.snapshot(), nestedState) {
		t.Fatalf("idempotent reopen = (%#v, %v)", reopened.snapshot(), err)
	}
}

func TestMalformedLegacyRuntimeStateIsSafelySkippedAndArchived(t *testing.T) {
	workspace := newRuntimeWorkspace(t)
	privateCanary := "private-runtime-state-canary"
	data := []byte(`{"last_channel":"` + privateCanary)
	path := filepath.Join(workspace, "state", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatalf("malformed legacy migration error = %v", err)
	}
	if got := manager.snapshot(); got != (State{}) {
		t.Fatalf("malformed legacy state = %#v, want empty", got)
	}
	archive := filepath.Join(
		workspace,
		"state",
		"legacy-json",
		runtimeLegacyArchiveLabel,
		"state",
		"state.json",
	)
	if archived, err := os.ReadFile(archive); err != nil || !bytes.Equal(archived, data) {
		t.Fatalf("malformed archive = %q, %v", archived, err)
	}
	db := openRawRuntimeDatabase(t, manager.databasePath)
	defer db.Close()
	var code string
	var digest []byte
	if err := db.QueryRow(`SELECT issue_code, record_digest FROM storage_import_issues
        WHERE component = ? AND source_id = ?`, runtimeDatabaseComponent, runtimeDirectorySourceID).Scan(
		&code,
		&digest,
	); err != nil {
		t.Fatal(err)
	}
	if code != "malformed-json" || len(digest) != sha256.Size || strings.Contains(code, privateCanary) {
		t.Fatalf("unsafe migration issue = code:%q digest:%x", code, digest)
	}
}

func TestRuntimeStateRetriesPendingArchiveWithoutReimport(t *testing.T) {
	workspace := newRuntimeWorkspace(t)
	data, err := json.Marshal(State{LastChannel: "pending:user"})
	if err != nil {
		t.Fatal(err)
	}
	preparePendingRuntimeArchive(t, workspace, data, sha256.Sum256(data))
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatalf("pending archive retry error = %v", err)
	}
	if got := manager.snapshot(); got != (State{}) {
		t.Fatalf("pending source was reimported: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(workspace, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("pending source still exists: %v", err)
	}
	archive := filepath.Join(
		workspace,
		"state",
		"legacy-json",
		runtimeLegacyArchiveLabel,
		"state.json",
	)
	if archived, err := os.ReadFile(archive); err != nil || !bytes.Equal(archived, data) {
		t.Fatalf("retried archive = %q, %v", archived, err)
	}
	db := openRawRuntimeDatabase(t, manager.databasePath)
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT archive_status FROM storage_imports
        WHERE component = ? AND source_id = ?`, runtimeDatabaseComponent, runtimeRootSourceID).Scan(
		&status,
	); err != nil {
		t.Fatal(err)
	}
	if status != "complete" {
		t.Fatalf("pending archive status = %q", status)
	}
}

func TestRuntimeStateRefusesChangedPendingArchiveSource(t *testing.T) {
	workspace := newRuntimeWorkspace(t)
	committed := []byte(`{"last_channel":"committed:user"}`)
	changed := []byte(`{"last_channel":"changed-private-user"}`)
	preparePendingRuntimeArchive(t, workspace, changed, sha256.Sum256(committed))
	if _, err := NewSQLiteManager(workspace); err == nil || !strings.Contains(err.Error(), "changed after import") {
		t.Fatalf("changed pending source error = %v", err)
	}
	path := filepath.Join(workspace, "state.json")
	if source, err := os.ReadFile(path); err != nil || !bytes.Equal(source, changed) {
		t.Fatalf("changed source was removed or modified = %q, %v", source, err)
	}
	archive := filepath.Join(
		workspace,
		"state",
		"legacy-json",
		runtimeLegacyArchiveLabel,
		"state.json",
	)
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("changed source was archived: %v", err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestSQLiteRuntimeStateRemainsAuthoritativeOverLateLegacySource(t *testing.T) {
	workspace := newRuntimeWorkspace(t)
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetLastChannel("sqlite:user"); err != nil {
		t.Fatal(err)
	}
	late := State{LastChannel: "legacy:user", LastChatID: "legacy-chat", Timestamp: time.Now().UTC()}
	writeLegacyRuntimeState(t, workspace, "state/state.json", late, 0o600)
	reopened, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.GetLastChannel() != "sqlite:user" || reopened.GetLastChatID() != "" {
		t.Fatalf("late legacy source replaced SQLite state: %#v", reopened.snapshot())
	}
	db := openRawRuntimeDatabase(t, manager.databasePath)
	defer db.Close()
	var skipped, authoritativeIssues int
	if err := db.QueryRow(`SELECT skipped_count FROM storage_imports
        WHERE component = ? AND source_id = ?`, runtimeDatabaseComponent, runtimeDirectorySourceID).Scan(
		&skipped,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM storage_import_issues
        WHERE component = ? AND source_id = ? AND issue_code = 'late-source'`,
		runtimeDatabaseComponent,
		runtimeDirectorySourceID,
	).Scan(&authoritativeIssues); err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || authoritativeIssues != 1 {
		t.Fatalf("late legacy audit = skipped:%d authoritative:%d", skipped, authoritativeIssues)
	}
}

func TestRuntimeStateRejectsUnsafeLegacyAndDatabaseBoundaries(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Run("unsafe legacy mode", func(t *testing.T) {
			workspace := newRuntimeWorkspace(t)
			writeLegacyRuntimeState(t, workspace, "state.json", State{}, 0o600)
			if err := os.Chmod(filepath.Join(workspace, "state.json"), 0o622); err != nil {
				t.Fatal(err)
			}
			if _, err := NewSQLiteManager(workspace); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("unsafe legacy error = %v", err)
			}
		})
	}

	t.Run("legacy symlink", func(t *testing.T) {
		workspace := newRuntimeWorkspace(t)
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(workspace, "state.json")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewSQLiteManager(workspace); err == nil ||
			(!strings.Contains(err.Error(), "unsafe") && !strings.Contains(err.Error(), "symlink")) {
			t.Fatalf("legacy symlink error = %v", err)
		}
	})

	t.Run("oversized legacy", func(t *testing.T) {
		workspace := newRuntimeWorkspace(t)
		path := filepath.Join(workspace, "state.json")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(runtimeLegacyMaxBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteManager(workspace); err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("oversized legacy error = %v", err)
		}
	})

	t.Run("database symlink", func(t *testing.T) {
		workspace := newRuntimeWorkspace(t)
		stateDirectory := filepath.Join(workspace, "state")
		if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.db")
		if err := os.WriteFile(outside, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(stateDirectory, runtimeDatabaseFilename)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewSQLiteManager(workspace); err == nil ||
			(!strings.Contains(err.Error(), "regular file") && !strings.Contains(err.Error(), "unsafe member")) {
			t.Fatalf("database symlink error = %v", err)
		}
	})

	t.Run("lock symlink", func(t *testing.T) {
		workspace := newRuntimeWorkspace(t)
		databasePath, err := resolveRuntimeDatabasePath(workspace)
		if err != nil {
			t.Fatal(err)
		}
		lockPath := runtimeDatabaseLockPath(databasePath) + ".lock"
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside-lock")
		if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, lockPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewSQLiteManager(workspace); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("lock symlink error = %v", err)
		}
		if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
			t.Fatalf("outside lock changed = %q, %v", data, err)
		}
	})
}

func TestRuntimeStateRejectsFutureInvalidAndCorruptDatabase(t *testing.T) {
	t.Run("future version", func(t *testing.T) {
		manager := mustSQLiteManager(t)
		raw := openRawRuntimeDatabase(t, manager.databasePath)
		if _, err := raw.Exec(`PRAGMA user_version = 2`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteManager(manager.workspace); !errors.Is(err, sqlitestore.ErrTooNew) {
			t.Fatalf("future schema error = %v", err)
		}
	})

	t.Run("invalid schema", func(t *testing.T) {
		manager := mustSQLiteManager(t)
		raw := openRawRuntimeDatabase(t, manager.databasePath)
		if _, err := raw.Exec(`CREATE TABLE rogue_runtime_state (value TEXT)`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteManager(manager.workspace); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("invalid schema error = %v", err)
		}
	})

	t.Run("unexpected unique index", func(t *testing.T) {
		manager := mustSQLiteManager(t)
		raw := openRawRuntimeDatabase(t, manager.databasePath)
		if _, err := raw.Exec(`CREATE UNIQUE INDEX rogue_runtime_channel
            ON runtime_state(last_channel)`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteManager(manager.workspace); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("unexpected unique-index error = %v", err)
		}
	})

	t.Run("missing singleton", func(t *testing.T) {
		manager := mustSQLiteManager(t)
		raw := openRawRuntimeDatabase(t, manager.databasePath)
		if _, err := raw.Exec(`DELETE FROM runtime_state`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteManager(manager.workspace); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("missing singleton error = %v", err)
		}
	})

	t.Run("corrupt database", func(t *testing.T) {
		workspace := newRuntimeWorkspace(t)
		databasePath, err := resolveRuntimeDatabasePath(workspace)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(databasePath, []byte("not SQLite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteManager(workspace); err == nil {
			t.Fatal("corrupt database was accepted")
		}
	})
}

func TestRuntimeStateValueValidationAndCompatibilityFailureBehavior(t *testing.T) {
	manager := mustSQLiteManager(t)
	for name, value := range map[string]string{
		"invalid UTF-8": string([]byte{0xff}),
		"NUL":           "bad\x00value",
		"oversized":     strings.Repeat("x", maxRuntimeStateValueBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := manager.SetLastChannel(value); err == nil {
				t.Fatalf("SetLastChannel(%s) error = nil", name)
			}
		})
	}
	manager.now = func() time.Time {
		return time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	if err := manager.SetLastChatID("chat"); err == nil || !strings.Contains(err.Error(), "supported range") {
		t.Fatalf("unsupported timestamp error = %v", err)
	}

	blockedWorkspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(blockedWorkspace, "state"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	compatibility := NewManager(blockedWorkspace)
	if compatibility == nil {
		t.Fatal("NewManager() returned nil on storage failure")
	}
	if compatibility.GetLastChannel() != "" || compatibility.GetLastChatID() != "" ||
		!compatibility.GetTimestamp().IsZero() {
		t.Fatalf("failed compatibility manager state = %#v", compatibility.snapshot())
	}
	if err := compatibility.SetLastChannel("channel"); err == nil {
		t.Fatal("compatibility setter storage error = nil")
	}

	var nilManager *Manager
	if err := nilManager.SetLastChannel("ignored"); err != nil || nilManager.GetLastChannel() != "" ||
		!nilManager.GetTimestamp().IsZero() {
		t.Fatalf(
			"nil manager behavior = channel:%q timestamp:%v error:%v",
			nilManager.GetLastChannel(),
			nilManager.GetTimestamp(),
			err,
		)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestRuntimeStateTransactionRollbackKeepsPriorValue(t *testing.T) {
	manager := mustSQLiteManager(t)
	if err := manager.SetLastChannel("before"); err != nil {
		t.Fatal(err)
	}
	db, unlock, err := manager.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	defer db.Close()
	injected := errors.New("injected rollback")
	err = sqlitestore.Immediate(t.Context(), db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(
			t.Context(),
			`UPDATE runtime_state SET last_channel = 'after', version = version + 1 WHERE id = 1`,
		); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("Immediate() error = %v", err)
	}
	var channel string
	if err := db.QueryRow(`SELECT last_channel FROM runtime_state WHERE id = 1`).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if channel != "before" {
		t.Fatalf("rolled-back channel = %q", channel)
	}
}

func TestRuntimeStateReadAndWriteFailuresReturnSafeCompatibilityValues(t *testing.T) {
	manager := mustSQLiteManager(t)
	raw := openRawRuntimeDatabase(t, manager.databasePath)
	if _, err := raw.Exec(`UPDATE runtime_state SET version = 9223372036854775807 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetLastChannel("cannot-overflow-version"); err == nil {
		t.Fatal("SetLastChannel() version-overflow error = nil")
	}

	raw = openRawRuntimeDatabase(t, manager.databasePath)
	if _, err := raw.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE runtime_state
        SET version = 1, updated_at_unix_seconds = 1, updated_at_nanosecond = NULL
        WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if state := manager.snapshot(); state != (State{}) {
		t.Fatalf("inconsistent stored state projection = %#v, want zero value", state)
	}
}

func TestRuntimeManagerConstructorFailureAndCompatibilityBranches(t *testing.T) {
	t.Run("successful compatibility constructor", func(t *testing.T) {
		manager := NewManager(newRuntimeWorkspace(t))
		if manager == nil || strings.TrimSpace(manager.databasePath) == "" {
			t.Fatalf("NewManager() = %#v", manager)
		}
	})

	t.Run("path resolution", func(t *testing.T) {
		injected := errors.New("injected absolute-path failure")
		previous := absoluteRuntimePath
		absoluteRuntimePath = func(string) (string, error) { return "", injected }
		t.Cleanup(func() { absoluteRuntimePath = previous })
		if _, err := NewSQLiteManager("workspace"); !errors.Is(err, injected) {
			t.Fatalf("NewSQLiteManager() path error = %v", err)
		}
		compatibility := NewManager("workspace")
		if compatibility == nil || compatibility.databasePath != "" {
			t.Fatalf("NewManager() path-failure manager = %#v", compatibility)
		}
		if _, _, err := compatibility.openDatabase(t.Context()); err == nil {
			t.Fatal("path-failure manager open error = nil")
		}
	})

	t.Run("blank workspace", func(t *testing.T) {
		if _, err := NewSQLiteManager(" "); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("NewSQLiteManager(blank) error = %v", err)
		}
		manager := NewManager("")
		if manager == nil || manager.databasePath != "" {
			t.Fatalf("NewManager(blank) = %#v", manager)
		}
		if err := manager.SetLastChannel("channel"); err == nil {
			t.Fatal("blank-workspace setter error = nil")
		}
	})

	t.Run("close failure", func(t *testing.T) {
		injected := errors.New("injected close failure")
		previous := closeInitializedRuntimeDatabase
		closeInitializedRuntimeDatabase = func(db *sql.DB) error {
			_ = db.Close()
			return injected
		}
		t.Cleanup(func() { closeInitializedRuntimeDatabase = previous })
		workspace := newRuntimeWorkspace(t)
		if _, err := NewSQLiteManager(workspace); !errors.Is(err, injected) {
			t.Fatalf("NewSQLiteManager() close error = %v", err)
		}
		if manager := NewManager(workspace); manager == nil {
			t.Fatal("NewManager() close failure returned nil")
		}
	})
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestRuntimeStateInternalErrorAndPriorityBoundaries(t *testing.T) {
	manager := mustSQLiteManager(t)
	db, unlock, err := manager.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, priorityOK := runtimeSourcePriority("unknown-source"); priorityOK {
		t.Fatal("unknown runtime source priority was accepted")
	}
	unknownInput := sqlitestore.LegacyInput{
		ID:       "unknown-source",
		Relative: "state.json",
		Data:     []byte(`{}`),
		Digest:   sha256.Sum256([]byte(`{}`)),
		Limit:    runtimeLegacyMaxBytes,
		Mode:     0o600,
	}
	if _, err := importLegacyRuntimeState(t.Context(), conn, unknownInput); err == nil {
		t.Fatal("unknown legacy source import error = nil")
	}

	invalidData := []byte(`{"last_channel":"bad\u0000channel"}`)
	invalidResult, err := importLegacyRuntimeState(t.Context(), conn, sqlitestore.LegacyInput{
		ID:       runtimeRootSourceID,
		Relative: "state.json",
		Data:     invalidData,
		Digest:   sha256.Sum256(invalidData),
		Limit:    runtimeLegacyMaxBytes,
		Mode:     0o600,
	})
	if err != nil || invalidResult.Skipped != 1 ||
		len(invalidResult.Issues) != 1 || invalidResult.Issues[0].Code != "invalid-state" {
		t.Fatalf("invalid legacy import = (%#v, %v)", invalidResult, err)
	}

	if _, _, valid := legacyRuntimeTimestampValues(
		time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
	); valid {
		t.Fatal("unsupported legacy timestamp was accepted")
	}
	if _, _, err := runtimeTimestampValues(time.Time{}); err != nil {
		t.Fatalf("zero runtime timestamp error = %v", err)
	}

	if _, err := conn.ExecContext(t.Context(), `UPDATE runtime_state
        SET origin_priority = 2 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	lowerData, err := json.Marshal(State{LastChannel: "lower:user"})
	if err != nil {
		t.Fatal(err)
	}
	lowerResult, err := importLegacyRuntimeState(t.Context(), conn, sqlitestore.LegacyInput{
		ID:       runtimeRootSourceID,
		Relative: "state.json",
		Data:     lowerData,
		Digest:   sha256.Sum256(lowerData),
		Limit:    runtimeLegacyMaxBytes,
		Mode:     0o600,
	})
	if err != nil || lowerResult.Skipped != 1 || lowerResult.Issues[0].Code != "lower-priority" {
		t.Fatalf("lower-priority import = (%#v, %v)", lowerResult, err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE runtime_state
        SET origin_priority = 0, version = 9223372036854775807 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	higherInput := sqlitestore.LegacyInput{
		ID:       runtimeDirectorySourceID,
		Relative: "state/state.json",
		Data:     lowerData,
		Digest:   sha256.Sum256(lowerData),
		Limit:    runtimeLegacyMaxBytes,
		Mode:     0o600,
	}
	if _, err := importLegacyRuntimeState(t.Context(), conn, higherInput); err == nil {
		t.Fatal("legacy import version-overflow error = nil")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE runtime_state
        SET origin_priority = 0, version = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `CREATE TEMP TRIGGER ignore_runtime_import
        BEFORE UPDATE ON runtime_state BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := importLegacyRuntimeState(t.Context(), conn, higherInput); !errors.Is(
		err,
		errRuntimeStateVersionChanged,
	) {
		t.Fatalf("ignored legacy import error = %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `DROP TRIGGER ignore_runtime_import`); err != nil {
		t.Fatal(err)
	}

	inconsistent := db.QueryRow(`SELECT '', '', 1, NULL, 1, 0`)
	if _, _, _, err := scanRuntimeState(inconsistent); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("inconsistent timestamp scan error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := scanRuntimeState(conn.QueryRowContext(t.Context(), selectRuntimeStateSQL)); err == nil {
		t.Fatal("scanRuntimeState(closed connection) error = nil")
	}
	if err := validateRuntimeSchema(t.Context(), conn); err == nil {
		t.Fatal("validateRuntimeSchema(closed connection) error = nil")
	}
	if _, err := importLegacyRuntimeState(t.Context(), conn, sqlitestore.LegacyInput{
		ID:       runtimeDirectorySourceID,
		Relative: "state/state.json",
		Data:     lowerData,
		Digest:   sha256.Sum256(lowerData),
		Limit:    runtimeLegacyMaxBytes,
		Mode:     0o600,
	}); err == nil {
		t.Fatal("importLegacyRuntimeState(closed connection) error = nil")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := lockRuntimeDatabase(""); err == nil {
		t.Fatal("lockRuntimeDatabase(empty) error = nil")
	}
	blockedLockRoot := newRuntimeWorkspace(t)
	blockedDatabase := filepath.Join(blockedLockRoot, "state", runtimeDatabaseFilename)
	if err := os.MkdirAll(filepath.Dir(blockedDatabase), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeDatabaseLockDirectory(blockedDatabase), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockRuntimeDatabase(blockedDatabase); err == nil {
		t.Fatal("lockRuntimeDatabase(blocked lock directory) error = nil")
	}
	var nilManager *Manager
	if _, _, err := nilManager.openDatabase(t.Context()); err == nil {
		t.Fatal("nil manager database open error = nil")
	}
}

func openRawRuntimeDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func mustSQLiteManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := NewSQLiteManager(newRuntimeWorkspace(t))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func writeLegacyRuntimeState(
	t *testing.T,
	workspace,
	relative string,
	state State,
	mode os.FileMode,
) []byte {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return data
}

func preparePendingRuntimeArchive(
	t *testing.T,
	workspace string,
	sourceData []byte,
	committedDigest [sha256.Size]byte,
) {
	t.Helper()
	databasePath, err := resolveRuntimeDatabasePath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	options, optionsErr := runtimeStoreOptions(workspace)
	if optionsErr != nil {
		t.Fatal(optionsErr)
	}
	options.Legacy = nil
	db, err := sqlitestore.Open(t.Context(), databasePath, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "state.json"), sourceData, 0o600); err != nil {
		t.Fatal(err)
	}
	raw := openRawRuntimeDatabase(t, databasePath)
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO storage_imports (
        component, source_id, source_relative, source_digest, source_size, source_limit,
        source_mode, imported_count, skipped_count, archive_status, imported_at
    ) VALUES (?, ?, 'state.json', ?, ?, ?, ?, 0, 0, 'pending', ?)`,
		runtimeDatabaseComponent,
		runtimeRootSourceID,
		committedDigest[:],
		len(sourceData),
		runtimeLegacyMaxBytes,
		0o600,
		time.Now().UTC().UnixNano(),
	); err != nil {
		t.Fatal(err)
	}
}

func newRuntimeWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return workspace
}
