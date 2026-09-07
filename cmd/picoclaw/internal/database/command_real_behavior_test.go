package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	matrixsqlite "github.com/sipeed/picoclaw/internal/channelstore/matrixstore/sqliteadapter"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func TestDatabaseStatusRealEnsureAndRefreshFailures(t *testing.T) {
	for _, name := range []string{"status", "shutdown"} {
		t.Run(name+" invalid home", func(t *testing.T) {
			t.Setenv(config.EnvHome, " invalid-home ")
			command := NewDatabaseCommand()
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs([]string{name})
			if err := command.Execute(); dblayer.CodeOf(err) != dblayer.CodeInvalid {
				t.Fatalf("%s invalid-home error = %v", name, err)
			}
		})
	}

	t.Run("status retries live manifest", func(t *testing.T) {
		var calls atomic.Int32
		_, _, server := startDatabaseCommandStatusServer(t, func(context.Context) ([]dblayer.StoreStatus, error) {
			if calls.Add(1) == 2 {
				return nil, errors.New("transient status failure")
			}
			return []dblayer.StoreStatus{}, nil
		})
		defer closeDatabaseCommandServer(t, server)
		command := NewDatabaseCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(io.Discard)
		command.SetArgs([]string{"status"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 3 || !strings.Contains(output.String(), server.Manifest().Epoch) {
			t.Fatalf("retried status calls=%d output=%q", calls.Load(), output.String())
		}
	})

	t.Run("status fails when refresh authority disappears", func(t *testing.T) {
		var calls atomic.Int32
		var home string
		var server *dblayer.Server
		home, _, server = startDatabaseCommandStatusServer(t, func(context.Context) ([]dblayer.StoreStatus, error) {
			if calls.Add(1) == 2 {
				stateDir, stateErr := dblayer.StateDirectory(home)
				if stateErr != nil {
					return nil, stateErr
				}
				if removeErr := os.Remove(filepath.Join(stateDir, "broker.json")); removeErr != nil {
					return nil, removeErr
				}
				return nil, errors.New("status failed after discovery disappeared")
			}
			return []dblayer.StoreStatus{}, nil
		})
		defer closeDatabaseCommandServer(t, server)
		command := NewDatabaseCommand()
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SetArgs([]string{"status"})
		if err := command.Execute(); err == nil {
			t.Fatal("status succeeded after refresh authority disappeared")
		}
	})

	t.Run("status propagates output failure", func(t *testing.T) {
		_, _, server := startDatabaseCommandStatusServer(t, func(context.Context) ([]dblayer.StoreStatus, error) {
			return []dblayer.StoreStatus{}, nil
		})
		defer closeDatabaseCommandServer(t, server)
		want := errors.New("status output failed")
		command := NewDatabaseCommand()
		command.SetOut(failingWriter{err: want})
		command.SetErr(io.Discard)
		command.SetArgs([]string{"status"})
		if err := command.Execute(); !errors.Is(err, want) {
			t.Fatalf("status output error = %v", err)
		}
	})
}

func startDatabaseCommandStatusServer(
	t *testing.T,
	statusProvider dblayer.StatusProvider,
) (string, string, *dblayer.Server) {
	t.Helper()
	home := t.TempDir()
	configPath := writeDatabaseCommandConfig(t, home, filepath.Join(home, "workspace"))
	_, fingerprint, err := dblayer.LoadCatalogConfiguration(configPath)
	if err != nil {
		t.Fatal(err)
	}
	server, err := dblayer.StartServer(t.Context(), dblayer.ServerOptions{
		Home: home, CatalogFingerprint: fingerprint, StatusProvider: statusProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)
	return home, configPath, server
}

func closeDatabaseCommandServer(t *testing.T, server *dblayer.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseMigrateReportsRealWorkspaceAndBackupFailures(t *testing.T) {
	t.Run("invalid configured workspace", func(t *testing.T) {
		home := t.TempDir()
		configPath := writeDatabaseCommandConfig(t, home, "bad\x00workspace")
		t.Setenv(config.EnvHome, home)
		t.Setenv(config.EnvConfig, configPath)

		command := NewDatabaseCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"migrate", "--dry-run"})
		if err := command.Execute(); dblayer.CodeOf(err) != dblayer.CodeInvalid {
			t.Fatalf("invalid workspace migration error = %v", err)
		}
	})

	t.Run("backup parent is a regular file", func(t *testing.T) {
		home := t.TempDir()
		workspace := filepath.Join(home, "workspace")
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		configPath := writeDatabaseCommandConfig(t, home, workspace)
		backupParent := filepath.Join(home, "not-a-directory")
		if err := os.WriteFile(backupParent, []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(config.EnvHome, home)
		t.Setenv(config.EnvConfig, configPath)

		command := NewDatabaseCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{
			"migrate", "--dry-run", "--store", "workspace/workflows",
			"--backup-dir", backupParent,
		})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "backup") {
			t.Fatalf("invalid backup parent migration error = %v", err)
		}
	})
}

func TestDatabaseServeHonorsRealOnlineAndPhysicalFences(t *testing.T) {
	t.Run("invalid configured workspace", func(t *testing.T) {
		home := t.TempDir()
		configPath := writeDatabaseCommandConfig(t, home, "bad\x00workspace")
		t.Setenv(config.EnvHome, home)
		t.Setenv(config.EnvConfig, configPath)
		prepareDatabaseServeBootstrap(t, home)

		command := NewDatabaseCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"__serve", "--home", home})
		if err := command.Execute(); dblayer.CodeOf(err) != dblayer.CodeInvalid {
			t.Fatalf("serve with invalid workspace error = %v", err)
		}
	})

	t.Run("offline migration owns home", func(t *testing.T) {
		home := t.TempDir()
		workspace := filepath.Join(home, "workspace")
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		configPath := writeDatabaseCommandConfig(t, home, workspace)
		t.Setenv(config.EnvHome, home)
		t.Setenv(config.EnvConfig, configPath)
		fence, err := dblayer.AcquireMigrationFence(home)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if closeErr := fence.Close(); closeErr != nil {
				t.Errorf("close migration fence: %v", closeErr)
			}
		}()
		prepareDatabaseServeBootstrap(t, home)

		command := NewDatabaseCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"__serve", "--home", home})
		if err := command.Execute(); dblayer.CodeOf(err) != dblayer.CodeConflict {
			t.Fatalf("serve with migration fence error = %v", err)
		}
	})

	t.Run("another owner holds catalog claims", func(t *testing.T) {
		home := t.TempDir()
		workspace := filepath.Join(home, "workspace")
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		configPath := writeDatabaseCommandConfig(t, home, workspace)
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		claims, err := dblayer.AcquireCatalogStoreClaims(home, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if closeErr := claims.Close(); closeErr != nil {
				t.Errorf("close catalog claims: %v", closeErr)
			}
		}()
		t.Setenv(config.EnvHome, home)
		t.Setenv(config.EnvConfig, configPath)
		prepareDatabaseServeBootstrap(t, home)

		command := NewDatabaseCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"__serve", "--home", home})
		if err := command.Execute(); dblayer.CodeOf(err) != dblayer.CodeConflict {
			t.Fatalf("serve with claimed stores error = %v", err)
		}
	})
}

func TestDatabaseServeTypedPreflightRejectsIncompleteReadyGenerations(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	configPath := filepath.Join(home, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	physical, err := storecatalog.Project(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoys := map[string]struct {
		version   int
		component string
	}{
		"account-routing":          {version: 1, component: "account-router"},
		"git-workspace-inventory":  {version: 2, component: "git-workspace-inventory"},
		"pr-workspace-checkpoints": {version: 3, component: "pr-workspace-checkpoints"},
	}
	decoyIDs := make(map[dblayer.StoreID]bool)
	for _, spec := range physical.All() {
		decoy, ok := decoys[spec.Domain]
		if !ok {
			continue
		}
		writeReadinessOnlyGeneration(t, spec.Path, decoy.version, decoy.component)
		id, parseErr := dblayer.ParseStoreID(spec.ID)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		decoyIDs[id] = true
	}
	if len(decoyIDs) != len(decoys) {
		t.Fatalf("created %d readiness decoys, want %d", len(decoyIDs), len(decoys))
	}

	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)
	prepareDatabaseServeBootstrap(t, home)
	command := NewDatabaseCommand()
	command.SetArgs([]string{"__serve", "--home", home})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- command.Execute() }()

	client := awaitDatabaseServeClient(t, home, done)
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, store := range status.Stores {
		if !decoyIDs[store.ID] {
			continue
		}
		seen++
		if store.Readiness == dblayer.StoreReady || store.Error == nil {
			t.Errorf("incomplete typed store remained ready: %#v", store)
		}
	}
	if seen != len(decoyIDs) {
		t.Fatalf("reported %d decoy statuses, want %d", seen, len(decoyIDs))
	}
	if err := client.Shutdown(t.Context()); err != nil && dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("database serve returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("database serve did not stop")
	}
}

func TestDatabaseServePreflightsConfiguredMatrixBridge(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	matrixRoot := filepath.Join(workspace, "matrix-real")
	if err := os.MkdirAll(matrixRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Channels = config.ChannelsConfig{
		"matrix-real": {
			Enabled: true,
			Type:    config.ChannelMatrix,
			Settings: config.RawNode(fmt.Sprintf(
				`{"crypto_database_path":%q,"crypto_passphrase":"coverage-passphrase"}`,
				matrixRoot,
			)),
		},
	}
	configPath := filepath.Join(home, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	fence, err := dblayer.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	migrateErr := matrixsqlite.MigrateDatabase(
		t.Context(),
		filepath.Join(matrixRoot, "store.db"),
	)
	closeFenceErr := fence.Close()
	if migrateErr != nil {
		t.Fatal(migrateErr)
	}
	if closeFenceErr != nil {
		t.Fatal(closeFenceErr)
	}

	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)
	prepareDatabaseServeBootstrap(t, home)
	command := NewDatabaseCommand()
	command.SetArgs([]string{"__serve", "--home", home})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- command.Execute() }()

	client := awaitDatabaseServeClient(t, home, done)
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantRawID, ok := storecatalog.ChannelStoreID(config.ChannelMatrix, "matrix-real")
	if !ok {
		t.Fatal("matrix channel did not resolve a logical store")
	}
	wantID, err := dblayer.ParseStoreID(wantRawID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, store := range status.Stores {
		if store.ID != wantID {
			continue
		}
		found = true
		if store.Readiness != dblayer.StoreReady || store.Error != nil {
			t.Fatalf("matrix bridge status = %#v", store)
		}
	}
	if !found {
		t.Fatalf("matrix bridge status %q is missing", wantID)
	}
	if err := client.Shutdown(t.Context()); err != nil && dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("database serve returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("database serve did not stop")
	}
}

func writeReadinessOnlyGeneration(t *testing.T, path string, version int, component string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := database.Exec(fmt.Sprintf(`
		PRAGMA user_version = %d;
		CREATE TABLE storage_imports (
			component TEXT NOT NULL,
			source_id TEXT NOT NULL,
			archive_status TEXT NOT NULL
		);
		CREATE TABLE storage_import_issues (component TEXT NOT NULL);
		CREATE TABLE storage_import_horizons (component TEXT PRIMARY KEY);
		CREATE INDEX storage_imports_archive_status_idx
			ON storage_imports(component, archive_status, source_id);
		INSERT INTO storage_import_horizons(component) VALUES (%q);
	`, version, component))
	closeErr := database.Close()
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func writeDatabaseCommandConfig(t *testing.T, home, workspace string) string {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	path := filepath.Join(home, "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}
