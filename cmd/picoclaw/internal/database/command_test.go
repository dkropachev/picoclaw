package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
	"github.com/sipeed/picoclaw/pkg/database/migration"
	backendapi "github.com/sipeed/picoclaw/web/backend/api"
)

func TestDatabaseCommandShapeAndHiddenServe(t *testing.T) {
	command := NewDatabaseCommand()
	if command.Use != "database" || !command.HasSubCommands() {
		t.Fatalf("database command = %#v", command)
	}
	wanted := map[string]bool{"status": false, "migrate": false, "shutdown": false, "__serve": false}
	for _, child := range command.Commands() {
		if _, ok := wanted[child.Name()]; ok {
			wanted[child.Name()] = true
		}
		if child.Name() == "__serve" && !child.Hidden {
			t.Fatal("private serve command is visible")
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("missing database subcommand %q", name)
		}
	}

	home := t.TempDir()
	command = NewDatabaseCommand()
	command.SetArgs([]string{"__serve", "--home", home})
	if err := command.Execute(); dblayer.CodeOf(err) != dblayer.CodeUnauthorized {
		t.Fatalf("direct private serve error = %v, want Unauthorized", err)
	}
}

func TestDatabaseStatusAndShutdownUseTypedBroker(t *testing.T) {
	home := t.TempDir()
	server, err := dblayer.StartServer(context.Background(), dblayer.ServerOptions{
		Home: home,
		StatusProvider: func(context.Context) ([]dblayer.StoreStatus, error) {
			return []dblayer.StoreStatus{{ID: "global.auth", Readiness: dblayer.StoreReady}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := dblayer.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	originalEnsure := ensureSupervisor
	ensureSupervisor = func(context.Context, dblayer.EnsureOptions) (*dblayer.Client, error) {
		return client, nil
	}
	t.Cleanup(func() { ensureSupervisor = originalEnsure })

	statusCommand := NewDatabaseCommand()
	var statusOutput bytes.Buffer
	statusCommand.SetOut(&statusOutput)
	statusCommand.SetArgs([]string{"status"})
	if err := statusCommand.Execute(); err != nil {
		t.Fatal(err)
	}
	var status dblayer.BrokerStatus
	if err := json.Unmarshal(statusOutput.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Epoch != server.Manifest().Epoch || len(status.Stores) != 1 {
		t.Fatalf("status = %#v", status)
	}

	shutdownCommand := NewDatabaseCommand()
	var shutdownOutput bytes.Buffer
	shutdownCommand.SetOut(&shutdownOutput)
	shutdownCommand.SetArgs([]string{"shutdown"})
	if err := shutdownCommand.Execute(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("broker did not shut down")
	}
}

func TestDatabaseMigrateDryRunAcceptsOnlyCatalogIDs(t *testing.T) {
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
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)

	command := NewDatabaseCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"migrate", "--dry-run", "--store", "workspace.workflows"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.DryRun {
		t.Fatalf("dry-run output = %s, error = %v", output.String(), err)
	}

	command = NewDatabaseCommand()
	command.SetArgs([]string{"migrate", "--dry-run", "--store", filepath.Join(home, "workflows.db")})
	if err := command.Execute(); err == nil {
		t.Fatal("migration accepted a physical database path")
	}
}

func TestDatabaseMigrateRefusesActiveBroker(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	if err := os.MkdirAll(cfg.Agents.Defaults.Workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)
	server, err := dblayer.StartServer(context.Background(), dblayer.ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	command := NewDatabaseCommand()
	command.SetArgs([]string{"migrate", "--dry-run"})
	err = command.Execute()
	if !errors.Is(err, migration.ErrStorageActive) {
		if err == nil || !strings.Contains(err.Error(), "database storage is active") {
			t.Fatalf("active migration error = %v", err)
		}
	}
}

func TestLazyDomainHandlerDoesNotOpenUntilDispatchAndClosesOnce(t *testing.T) {
	var opened, closed, handled atomic.Int64
	lazy := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
		opened.Add(1)
		return dblayer.HandlerFunc(func(context.Context, dblayer.Request) (any, error) {
				handled.Add(1)
				return map[string]bool{"ok": true}, nil
			}), func() error {
				closed.Add(1)
				return nil
			}, nil
	})
	if opened.Load() != 0 {
		t.Fatal("lazy domain opened during construction")
	}
	payload, err := dblayer.MarshalCanonical(dblayer.EmptyPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lazy.Handle(t.Context(), dblayer.Request{
		Domain: "test", Version: 1, Operation: "read", Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 1 || handled.Load() != 1 {
		t.Fatalf("lazy counts = open:%d handle:%d", opened.Load(), handled.Load())
	}
	if err := lazy.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lazy.Close(); err != nil || closed.Load() != 1 {
		t.Fatalf("lazy close = %v count:%d", err, closed.Load())
	}
}

func TestLazyDomainHandlerEnsureOpenProvesReadinessWithoutDispatch(t *testing.T) {
	var opened, closed, handled atomic.Int64
	lazy := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
		opened.Add(1)
		return dblayer.HandlerFunc(func(context.Context, dblayer.Request) (any, error) {
				handled.Add(1)
				return nil, nil
			}), func() error {
				closed.Add(1)
				return nil
			}, nil
	})
	if err := lazy.ensureOpen(); err != nil {
		t.Fatal(err)
	}
	if err := lazy.ensureOpen(); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 1 || handled.Load() != 0 {
		t.Fatalf("readiness counts = open:%d handle:%d", opened.Load(), handled.Load())
	}
	if err := lazy.Close(); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("readiness close count = %d", closed.Load())
	}
}

func TestLazyDomainHandlerCloseBeforeUseNeverOpens(t *testing.T) {
	var opened atomic.Int64
	lazy := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
		opened.Add(1)
		return nil, nil, nil
	})
	if err := lazy.Close(); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 0 {
		t.Fatal("closing unused lazy domain opened storage")
	}
}

func TestOptionalModelCatalogTypedPreflightUpdatesReadiness(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	fence, fenceErr := dblayer.AcquireMigrationFence(home)
	if fenceErr != nil {
		t.Fatal(fenceErr)
	}
	if migrateErr := backendapi.RunOfflineModelCatalogMigration(t.Context(), home); migrateErr != nil {
		_ = fence.Close()
		t.Fatal(migrateErr)
	}
	if closeFenceErr := fence.Close(); closeFenceErr != nil {
		t.Fatal(closeFenceErr)
	}
	path := filepath.Join(home, "model-catalogs.db")
	database, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, executeErr := database.Exec(`DROP INDEX model_catalogs_provider_idx`); executeErr != nil {
		_ = database.Close()
		t.Fatal(executeErr)
	}
	if closeDatabaseErr := database.Close(); closeDatabaseErr != nil {
		t.Fatal(closeDatabaseErr)
	}
	statuses, err := dbcatalog.ProbeStatuses(t.Context(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbcatalog.CloseProbePools(home) })
	logical, err := dbcatalog.New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	handler := backendapi.NewModelCatalogBrokerHandler(home)
	t.Cleanup(func() { _ = handler.Close() })
	statuses, err = dbcatalog.InitializeRequired(
		t.Context(), logical, statuses,
		func(ctx context.Context, entry dbcatalog.Entry) error {
			if entry.Domain != "model-catalogs" {
				return nil
			}
			return preflightBrokerTarget(
				ctx, handler, "model-catalogs", "preflight", entry.ID,
			)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.ID != backendapi.ModelCatalogStoreID {
			continue
		}
		if status.Readiness != dblayer.StoreIntegrityFailed ||
			status.Error == nil || status.Error.Code != dblayer.CodeIntegrity {
			t.Fatalf("optional model catalog readiness = %#v", status)
		}
		return
	}
	t.Fatal("optional model catalog status is missing")
}
