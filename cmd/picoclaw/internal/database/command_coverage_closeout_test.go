package database

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlbridge"
	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
)

func TestDatabaseCommandConfigurationAndWorkspaceBoundaries(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "missing.json")
	if cfg, err := loadDatabaseConfig(missing); err != nil || cfg == nil {
		t.Fatalf("missing config = %#v, %v", cfg, err)
	}
	broken := filepath.Join(home, "broken.json")
	if err := os.WriteFile(broken, []byte(`{"agents":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := loadDatabaseConfig(broken); err == nil || cfg != nil {
		t.Fatalf("broken config = %#v, %v", cfg, err)
	}

	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	for _, test := range []struct {
		configured string
		want       string
	}{
		{"", filepath.Join(home, "workspace")},
		{"relative", filepath.Join(home, "relative")},
		{"~", userHome},
		{"~/nested", filepath.Join(userHome, "nested")},
		{filepath.Join(home, "absolute"), filepath.Join(home, "absolute")},
	} {
		got, err := trustedWorkspace(home, test.configured)
		if err != nil || got != test.want {
			t.Errorf("trustedWorkspace(%q) = %q, %v; want %q", test.configured, got, err, test.want)
		}
	}
	if _, err := trustedWorkspace(home, "bad\x00workspace"); dblayer.CodeOf(err) != dblayer.CodeInvalid {
		t.Fatalf("NUL workspace error = %v", err)
	}
	t.Setenv("HOME", "")
	if _, err := trustedWorkspace(home, "~"); dblayer.CodeOf(err) != dblayer.CodeInvalid {
		t.Fatalf("home-less workspace error = %v", err)
	}
}

func TestPreflightHelpersUseTypedStoreTargets(t *testing.T) {
	var brokerCalls, bridgeCalls atomic.Int64
	broker := dblayer.HandlerFunc(func(_ context.Context, request dblayer.Request) (any, error) {
		brokerCalls.Add(1)
		if request.Domain != "domain" || request.Version != 1 || request.Operation != "preflight" {
			t.Fatalf("broker preflight request = %#v", request)
		}
		var payload struct {
			StoreID dblayer.StoreID `json:"store_id"`
		}
		if err := dblayer.UnmarshalCanonical(
			request.Payload,
			&payload,
		); err != nil || payload.StoreID != "global.auth" {
			t.Fatalf("broker preflight payload = %#v, %v", payload, err)
		}
		return dblayer.EmptyPayload{}, nil
	})
	if err := preflightBrokerTarget(t.Context(), broker, "domain", "preflight", "global.auth"); err != nil {
		t.Fatal(err)
	}

	bridge := dblayer.HandlerFunc(func(_ context.Context, request dblayer.Request) (any, error) {
		bridgeCalls.Add(1)
		if request.Domain != sqlbridge.RPCDomain || request.Version != sqlbridge.RPCVersion ||
			request.Operation != sqlbridge.RPCOperationPing {
			t.Fatalf("SQL bridge preflight request = %#v", request)
		}
		var payload sqlbridge.PingRequest
		if err := dblayer.UnmarshalCanonical(request.Payload, &payload); err != nil ||
			payload.Target.StoreID != "channel.matrix.main" || payload.Target.Mode != sqlbridge.ModeRuntime {
			t.Fatalf("SQL bridge preflight payload = %#v, %v", payload, err)
		}
		return sqlbridge.PingResponse{}, nil
	})
	if err := preflightSQLBridge(t.Context(), bridge, "channel.matrix.main"); err != nil {
		t.Fatal(err)
	}
	if brokerCalls.Load() != 1 || bridgeCalls.Load() != 1 {
		t.Fatalf("preflight calls = broker:%d bridge:%d", brokerCalls.Load(), bridgeCalls.Load())
	}
}

func TestLazyDomainHandlerFailureClassification(t *testing.T) {
	_, err := (*lazyDomainHandler)(nil).Handle(t.Context(), dblayer.Request{})
	if dblayer.CodeOf(err) != dblayer.CodeUnavailable {
		t.Fatalf("nil lazy Handle error = %v", err)
	}
	if err := (*lazyDomainHandler)(nil).ensureOpen(); dblayer.CodeOf(err) != dblayer.CodeUnavailable {
		t.Fatalf("nil lazy ensureOpen error = %v", err)
	}
	if err := (*lazyDomainHandler)(nil).Close(); err != nil {
		t.Fatalf("nil lazy Close error = %v", err)
	}

	for name, test := range map[string]struct {
		openErr  error
		wantCode dblayer.ErrorCode
	}{
		"invalid schema": {sqlitestore.ErrInvalidSchema, dblayer.CodeIntegrity},
		"integrity":      {sqlitestore.ErrIntegrity, dblayer.CodeIntegrity},
		"too new":        {sqlitestore.ErrTooNew, dblayer.CodeUnsupported},
		"domain code":    {dblayer.NewError(dblayer.CodeMigrationRequired, "old"), dblayer.CodeMigrationRequired},
		"internal":       {errors.New("open failed"), dblayer.CodeInternal},
	} {
		t.Run(name, func(t *testing.T) {
			lazy := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				return nil, nil, test.openErr
			})
			if err := lazy.ensureOpen(); dblayer.CodeOf(err) != test.wantCode {
				t.Fatalf("ensureOpen error = %v, want %s", err, test.wantCode)
			}
			if _, err := lazy.Handle(t.Context(), dblayer.Request{}); dblayer.CodeOf(err) != test.wantCode {
				t.Fatalf("Handle error = %v, want %s", err, test.wantCode)
			}
		})
	}

	empty := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) { return nil, nil, nil })
	if err := empty.ensureOpen(); dblayer.CodeOf(err) != dblayer.CodeUnavailable {
		t.Fatalf("empty lazy handler error = %v", err)
	}
	closed := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
		return dblayer.HandlerFunc(func(context.Context, dblayer.Request) (any, error) { return nil, nil }), nil, nil
	})
	if err := closed.ensureOpen(); err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.Handle(t.Context(), dblayer.Request{}); dblayer.CodeOf(err) != dblayer.CodeUnavailable {
		t.Fatalf("closed lazy handler error = %v", err)
	}
	wantClose := errors.New("close failed")
	closeFailure := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
		return dblayer.HandlerFunc(func(context.Context, dblayer.Request) (any, error) { return nil, nil }),
			func() error { return wantClose }, nil
	})
	if err := closeFailure.ensureOpen(); err != nil {
		t.Fatal(err)
	}
	if err := closeFailure.Close(); !errors.Is(err, wantClose) {
		t.Fatalf("close failure = %v", err)
	}
}

func TestDatabaseServeOwnsTypedDomainRouter(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	configPath := filepath.Join(home, "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
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
	var response dblayer.EmptyPayload
	for _, domain := range []string{
		"launcher-auth", "auth", "model-catalogs", "workflows", "cron", "account-routing",
		"sessions", "eventing", "evolution", "repository-reviews", "repository-evaluations",
		"runtime-state", "seahorse", localci.CacheBrokerDomain, "channel-wecom", "channel-weixin",
		"tool-adaptation", sqlbridge.RPCDomain, "git-workspace-inventory", "pr-workspace-checkpoints",
	} {
		err := client.Call(
			t.Context(), domain, 1, "coverage-unsupported", dblayer.EmptyPayload{}, &response,
		)
		if err == nil {
			t.Errorf("domain %q accepted an unsupported operation", domain)
		}
	}
	if err := client.Call(
		t.Context(), "unsupported-test-domain", 1, "read", dblayer.EmptyPayload{}, &response,
	); dblayer.CodeOf(err) != dblayer.CodeUnsupported {
		t.Fatalf("unsupported routed domain error = %v", err)
	}
	status, err := client.Status(t.Context())
	if err != nil || status.Epoch == "" || len(status.Stores) == 0 {
		t.Fatalf("serve status = %#v, %v", status, err)
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

func prepareDatabaseServeBootstrap(t *testing.T, home string) {
	t.Helper()
	canonical, err := dblayer.PrepareHome(home)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(canonical, dblayer.StateDirectoryName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	path := filepath.Join(stateDir, ".bootstrap-"+token)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICOCLAW_DATABASE_SUPERVISOR_BOOTSTRAP", token)
}

func awaitDatabaseServeClient(t *testing.T, home string, done <-chan error) *dblayer.Client {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("database serve exited before readiness: %v", err)
		case <-deadline.C:
			t.Fatal("database serve did not publish readiness")
		case <-ticker.C:
			client, err := dblayer.Connect(home)
			if err != nil {
				continue
			}
			pingCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, err = client.Ping(pingCtx)
			cancel()
			if err == nil {
				return client
			}
		}
	}
}

type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestDatabaseCommandOutputErrors(t *testing.T) {
	want := errors.New("write failed")
	command := NewDatabaseCommand()
	command.SetOut(failingWriter{err: want})
	if err := writeJSON(command, map[string]bool{"ok": true}); !errors.Is(err, want) {
		t.Fatalf("writeJSON error = %v", err)
	}
}

func TestDatabaseCommandsReturnMigrationInputErrors(t *testing.T) {
	t.Setenv(config.EnvHome, " invalid-home ")
	command := NewDatabaseCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"migrate", "--dry-run"})
	if err := command.Execute(); dblayer.CodeOf(err) != dblayer.CodeInvalid {
		t.Fatalf("invalid migration home error = %v", err)
	}

	home := t.TempDir()
	broken := filepath.Join(home, "broken.json")
	if err := os.WriteFile(broken, []byte(`{"agents":`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, broken)
	command = NewDatabaseCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"migrate", "--dry-run"})
	if err := command.Execute(); err == nil {
		t.Fatal("migration accepted malformed config")
	}
}

func TestLazyDomainHandlerWithoutOpenerFailsClosed(t *testing.T) {
	lazy := &lazyDomainHandler{}
	if err := lazy.ensureOpen(); dblayer.CodeOf(err) != dblayer.CodeUnavailable {
		t.Fatalf("zero lazy handler error = %v", err)
	}
}

func TestLazyDomainHandlerRejectsPostOpenClosedState(t *testing.T) {
	handler := &lazyDomainHandler{
		handler: dblayer.HandlerFunc(func(context.Context, dblayer.Request) (any, error) {
			return dblayer.EmptyPayload{}, nil
		}),
		closed: true,
	}
	handler.once.Do(func() {})
	if _, err := handler.Handle(t.Context(), dblayer.Request{}); dblayer.CodeOf(err) != dblayer.CodeUnavailable {
		t.Fatalf("closed post-open handler = %v", err)
	}
}

func TestDatabaseServeRejectsInvalidLoadedConfiguration(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"agents":`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)
	prepareDatabaseServeBootstrap(t, home)
	command := NewDatabaseCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"__serve", "--home", home})
	if err := command.Execute(); err == nil {
		t.Fatal("database serve accepted malformed config")
	}
}
