package gateway

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestPrepareGatewayRuntimeInvocationAuthorityBoundaries(t *testing.T) {
	preparedRuntimeAuthority.Lock()
	preparedRuntimeAuthority.client = nil
	preparedRuntimeAuthority.home = ""
	preparedRuntimeAuthority.Unlock()

	t.Setenv(gatewayRuntimeChildEnvironment, "")
	if child, err := PrepareGatewayRuntimeInvocation(t.Context()); child || err != nil {
		t.Fatalf("ordinary invocation = child:%t err:%v", child, err)
	}
	t.Setenv(gatewayRuntimeChildEnvironment, "1")
	t.Setenv("PICOCLAW_DATABASE_AUTHORITY", "")
	if child, err := PrepareGatewayRuntimeInvocation(t.Context()); !child || err == nil {
		t.Fatalf("missing child authority = child:%t err:%v", child, err)
	}

	home := t.TempDir()
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGatewayTestServer(server) })
	authority, err := database.InheritedAuthorityEnvironment(home)
	if err != nil {
		t.Fatal(err)
	}
	name, value, ok := strings.Cut(authority, "=")
	if !ok {
		t.Fatalf("inherited authority = %q", authority)
	}
	t.Setenv(name, value)
	t.Setenv(config.EnvHome, home)
	t.Setenv(gatewayRuntimeChildEnvironment, "1")
	if child, err := PrepareGatewayRuntimeInvocation(t.Context()); !child || err != nil {
		t.Fatalf("valid child authority = child:%t err:%v", child, err)
	}
	client, preparedHome, prepared := takePreparedRuntimeAuthority()
	if !prepared || client == nil || preparedHome != home {
		t.Fatalf("prepared authority = client:%p home:%q prepared:%t", client, preparedHome, prepared)
	}
	secondClient, secondHome, secondPrepared := takePreparedRuntimeAuthority()
	if secondPrepared || secondClient != nil || secondHome != "" {
		t.Fatalf(
			"replayed prepared authority = client:%p home:%q prepared:%t",
			secondClient,
			secondHome,
			secondPrepared,
		)
	}
}

func TestPrepareGatewayRuntimeInvocationRejectsHomeMismatch(t *testing.T) {
	home := t.TempDir()
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGatewayTestServer(server) })
	authority, err := database.InheritedAuthorityEnvironment(home)
	if err != nil {
		t.Fatal(err)
	}
	name, value, _ := strings.Cut(authority, "=")
	t.Setenv(name, value)
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(gatewayRuntimeChildEnvironment, "1")
	if child, err := PrepareGatewayRuntimeInvocation(t.Context()); !child ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("mismatched child authority = child:%t err:%v", child, err)
	}
}

func TestGatewayRuntimeChildPropagatesFlagsAndAuthority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper is Unix-only")
	}
	output := filepath.Join(t.TempDir(), "child.txt")
	t.Setenv("PICOCLAW_TEST_CHILD_OUTPUT", output)
	executable := writeGatewayChildScript(t, `
printf '%s\n' "$*" "$PICOCLAW_GATEWAY_RUNTIME_CHILD" "$PICOCLAW_GATEWAY_SUPERVISOR_PID" "$PICOCLAW_DATABASE_AUTHORITY" > "$PICOCLAW_TEST_CHILD_OUTPUT"
`)
	command := &cobra.Command{}
	command.SetIn(strings.NewReader("input"))
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	client := connectGatewayTestClient(t)
	if err := runGatewayRuntimeChild(
		t.Context(), command, client, executable,
		"PICOCLAW_DATABASE_AUTHORITY=encoded-authority", true, true, true,
	); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 || lines[0] != "gateway --debug --no-truncate --allow-empty" ||
		lines[1] != "1" || lines[2] != strconv.Itoa(os.Getpid()) || lines[3] != "encoded-authority" {
		t.Fatalf("runtime child projection = %q", raw)
	}

	if err := runGatewayRuntimeChild(
		t.Context(), command, client, filepath.Join(t.TempDir(), "missing"), "invalid", false, false, false,
	); err == nil {
		t.Fatal("missing runtime executable started")
	}
}

func TestGatewayRuntimeChildStopsOnContextAndBrokerLoss(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper is Unix-only")
	}
	executable := writeGatewayChildScript(t, `
trap 'exit 0' TERM
while :; do sleep 1; done
`)
	command := &cobra.Command{}
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	client := connectGatewayTestClient(t)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if err := runGatewayRuntimeChild(
		ctx, command, client, executable, "PICOCLAW_DATABASE_AUTHORITY=value", false, false, false,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled runtime child error = %v", err)
	}

	home := t.TempDir()
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	lostClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, func() { closeGatewayTestServer(server) })
	err = runGatewayRuntimeChild(
		t.Context(), command, lostClient, executable,
		"PICOCLAW_DATABASE_AUTHORITY=value", false, false, false,
	)
	if err == nil {
		t.Fatal("runtime child survived broker loss")
	}
}

func TestRuntimeBrokerMonitorAndEnvironmentReplacement(t *testing.T) {
	client := connectGatewayTestClient(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		monitorRuntimeBroker(client, stop)
		close(done)
	}()
	time.Sleep(1100 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime broker monitor did not stop")
	}

	environment := replaceEnvironment([]string{"KEEP=one", "TARGET=old", "TARGET=duplicate"}, "TARGET", "new")
	if strings.Join(environment, ",") != "KEEP=one,TARGET=new" {
		t.Fatalf("replaced environment = %#v", environment)
	}
}

func TestGatewayCommandPreRunFlagContract(t *testing.T) {
	command := NewGatewayCommand()
	if err := command.Flags().Set("no-truncate", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.PreRunE(command, nil); err == nil {
		t.Fatal("no-truncate without debug was accepted")
	}
	command = NewGatewayCommand()
	if err := command.Flags().Set("debug", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("no-truncate", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.PreRunE(command, nil); err != nil {
		t.Fatalf("debug no-truncate pre-run: %v", err)
	}
}

func TestGatewayCommandProjectsHostAndChildMode(t *testing.T) {
	previousRuntime := runGatewayRuntimeCommand
	previousSupervisor := runGatewaySupervisorCommand
	t.Cleanup(func() {
		runGatewayRuntimeCommand = previousRuntime
		runGatewaySupervisorCommand = previousSupervisor
	})
	var supervisorCalls int
	runGatewaySupervisorCommand = func(_ *cobra.Command, debug, noTruncate, allowEmpty bool) error {
		supervisorCalls++
		if debug || noTruncate || allowEmpty || os.Getenv(config.EnvGatewayHost) != "127.0.0.1" {
			t.Fatalf("supervised command projection = %t/%t/%t host=%q",
				debug, noTruncate, allowEmpty, os.Getenv(config.EnvGatewayHost))
		}
		return nil
	}
	t.Setenv(config.EnvGatewayHost, "previous")
	command := NewGatewayCommand()
	command.SetArgs([]string{"--host", "127.0.0.1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if supervisorCalls != 1 || os.Getenv(config.EnvGatewayHost) != "previous" {
		t.Fatalf("supervised calls=%d restored host=%q", supervisorCalls, os.Getenv(config.EnvGatewayHost))
	}

	command = NewGatewayCommand()
	command.SetArgs([]string{"--host", " "})
	if err := command.Execute(); err == nil {
		t.Fatal("gateway command accepted blank explicit host")
	}

	var runtimeCalls int
	runGatewayRuntimeCommand = func(context.Context, bool, bool) error {
		runtimeCalls++
		if os.Getenv(gatewayRuntimeChildEnvironment) != "" {
			t.Fatal("runtime child marker was not consumed")
		}
		return nil
	}
	t.Setenv(gatewayRuntimeChildEnvironment, "1")
	command = NewGatewayCommand()
	command.SetArgs(nil)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if runtimeCalls != 1 {
		t.Fatalf("authenticated runtime calls = %d", runtimeCalls)
	}
}

func TestRunAuthenticatedGatewayRuntimeUsesPreparedBroker(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	client, _ := startGatewaySupervisorClient(t, home)
	preparedRuntimeAuthority.Lock()
	preparedRuntimeAuthority.client = client
	preparedRuntimeAuthority.home = home
	preparedRuntimeAuthority.Unlock()
	previousCore := runGatewayCore
	t.Cleanup(func() {
		runGatewayCore = previousCore
		preparedRuntimeAuthority.Lock()
		preparedRuntimeAuthority.client = nil
		preparedRuntimeAuthority.home = ""
		preparedRuntimeAuthority.Unlock()
	})
	var called bool
	runGatewayCore = func(debug bool, gotHome, _ string, allowEmpty bool) error {
		called = true
		if !debug || !allowEmpty || gotHome != home {
			t.Fatalf("core gateway args = debug:%t allow-empty:%t home:%q", debug, allowEmpty, gotHome)
		}
		return nil
	}
	if err := runAuthenticatedGatewayRuntime(t.Context(), true, true); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("core gateway was not invoked")
	}
}

func TestRunSupervisedGatewaySuccessAndFailureBoundaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	client, server := startGatewaySupervisorClient(t, home)
	installGatewaySupervisorSeams(t)
	resolveGatewayRuntimeExecutable = func() (string, error) { return "/test/picoclaw", nil }
	ensureGatewaySupervisor = func(_ context.Context, options database.EnsureOptions) (*database.Client, error) {
		if options.Home != home || options.Executable != "/test/picoclaw" {
			t.Fatalf("supervisor options = %#v", options)
		}
		return client, nil
	}
	gatewayInheritedAuthority = func(gotHome string) (string, error) {
		if gotHome != home {
			t.Fatalf("authority home = %q", gotHome)
		}
		return "PICOCLAW_DATABASE_AUTHORITY=test", nil
	}
	var calls int
	runGatewayChild = func(
		_ context.Context,
		_ *cobra.Command,
		gotClient *database.Client,
		executable,
		authority string,
		debug,
		noTruncate,
		allowEmpty bool,
	) error {
		calls++
		if gotClient != client || executable != "/test/picoclaw" ||
			authority != "PICOCLAW_DATABASE_AUTHORITY=test" || !debug || !noTruncate || !allowEmpty {
			t.Fatalf("runtime child args = client:%p executable:%q authority:%q flags:%t/%t/%t",
				gotClient, executable, authority, debug, noTruncate, allowEmpty)
		}
		return nil
	}
	command := &cobra.Command{}
	command.SetContext(t.Context())
	if err := runSupervisedGateway(command, true, true, true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("runtime child calls = %d", calls)
	}
	closeGatewayTestServer(server)

	want := errors.New("resolve executable")
	resolveGatewayRuntimeExecutable = func() (string, error) { return "", want }
	if err := runSupervisedGateway(command, false, false, false); !errors.Is(err, want) {
		t.Fatalf("executable resolution error = %v", err)
	}
	resolveGatewayRuntimeExecutable = func() (string, error) { return "/test/picoclaw", nil }
	ensureGatewaySupervisor = func(context.Context, database.EnsureOptions) (*database.Client, error) {
		return nil, want
	}
	if err := runSupervisedGateway(command, false, false, false); !errors.Is(err, want) {
		t.Fatalf("supervisor startup error = %v", err)
	}
}

func TestRunSupervisedGatewayBoundsHealthyRuntimeRestarts(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	client, _ := startGatewaySupervisorClient(t, home)
	installGatewaySupervisorSeams(t)
	resolveGatewayRuntimeExecutable = func() (string, error) { return "/test/picoclaw", nil }
	ensureGatewaySupervisor = func(context.Context, database.EnsureOptions) (*database.Client, error) {
		return client, nil
	}
	gatewayInheritedAuthority = func(string) (string, error) {
		return "PICOCLAW_DATABASE_AUTHORITY=test", nil
	}
	want := errors.New("runtime exited")
	var calls int
	runGatewayChild = func(
		context.Context, *cobra.Command, *database.Client, string, string, bool, bool, bool,
	) error {
		calls++
		return want
	}
	command := &cobra.Command{}
	command.SetContext(t.Context())
	if err := runSupervisedGateway(command, false, false, false); !errors.Is(err, want) ||
		!strings.Contains(err.Error(), "repeatedly exited") {
		t.Fatalf("bounded restart error = %v", err)
	}
	if calls != 5 {
		t.Fatalf("runtime restart calls = %d", calls)
	}
}

func TestRunSupervisedGatewayRecoversLostBroker(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	firstClient, firstServer := startGatewaySupervisorClient(t, home)
	installGatewaySupervisorSeams(t)
	resolveGatewayRuntimeExecutable = func() (string, error) { return "/test/picoclaw", nil }
	var ensureCalls int
	var secondClient *database.Client
	ensureGatewaySupervisor = func(context.Context, database.EnsureOptions) (*database.Client, error) {
		ensureCalls++
		if ensureCalls == 1 {
			return firstClient, nil
		}
		if secondClient == nil {
			var secondServer *database.Server
			secondClient, secondServer = startGatewaySupervisorClient(t, home)
			t.Cleanup(func() { closeGatewayTestServer(secondServer) })
		}
		return secondClient, nil
	}
	var authorityCalls int
	gatewayInheritedAuthority = func(string) (string, error) {
		authorityCalls++
		return "PICOCLAW_DATABASE_AUTHORITY=test", nil
	}
	var childCalls int
	runGatewayChild = func(
		_ context.Context,
		_ *cobra.Command,
		client *database.Client,
		_, _ string,
		_, _, _ bool,
	) error {
		childCalls++
		if childCalls == 1 {
			if client != firstClient {
				t.Fatal("first runtime did not receive first broker")
			}
			closeGatewayTestServer(firstServer)
			return errors.New("broker lost")
		}
		if client != secondClient {
			t.Fatal("recovered runtime did not receive replacement broker")
		}
		return nil
	}
	command := &cobra.Command{}
	command.SetContext(t.Context())
	if err := runSupervisedGateway(command, false, false, false); err != nil {
		t.Fatal(err)
	}
	if childCalls != 2 || ensureCalls < 2 || authorityCalls != 2 {
		t.Fatalf("recovery calls = child:%d ensure:%d authority:%d", childCalls, ensureCalls, authorityCalls)
	}
}

func TestRecoverGatewaySupervisorHonorsCancellation(t *testing.T) {
	installGatewaySupervisorSeams(t)
	want := errors.New("unavailable")
	ensureGatewaySupervisor = func(context.Context, database.EnsureOptions) (*database.Client, error) {
		return nil, want
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client, err := recoverGatewayDatabaseSupervisor(ctx, t.TempDir(), "/test/picoclaw")
	if client != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery = client:%p err:%v", client, err)
	}
}

func TestRunSupervisedGatewayAdmissionAndRecoveryErrors(t *testing.T) {
	installGatewaySupervisorSeams(t)
	command := &cobra.Command{}
	command.SetContext(t.Context())
	t.Setenv(config.EnvHome, " invalid-home ")
	if err := runSupervisedGateway(command, false, false, false); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid supervised home error = %v", err)
	}

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home:           home,
		RequiredStores: []database.StoreID{"test.required"},
		StatusProvider: func(context.Context) ([]database.StoreStatus, error) {
			return []database.StoreStatus{{
				ID: "test.required", Readiness: database.StoreMigrationRequired,
				Error: database.NewError(database.CodeMigrationRequired, "offline migration required"),
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGatewayTestServer(server) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	resolveGatewayRuntimeExecutable = func() (string, error) { return "/test/picoclaw", nil }
	ensureGatewaySupervisor = func(context.Context, database.EnsureOptions) (*database.Client, error) {
		return client, nil
	}
	err = runSupervisedGateway(command, false, false, false)
	if database.CodeOf(err) != database.CodeMigrationRequired {
		t.Fatalf("not-ready supervised gateway error = %v", err)
	}

	readyHome := t.TempDir()
	t.Setenv(config.EnvHome, readyHome)
	readyClient, _ := startGatewaySupervisorClient(t, readyHome)
	ensureGatewaySupervisor = func(context.Context, database.EnsureOptions) (*database.Client, error) {
		return readyClient, nil
	}
	want := errors.New("authority unavailable")
	gatewayInheritedAuthority = func(string) (string, error) { return "", want }
	if err := runSupervisedGateway(command, false, false, false); !errors.Is(err, want) {
		t.Fatalf("authority projection error = %v", err)
	}

	gatewayInheritedAuthority = func(string) (string, error) {
		return "PICOCLAW_DATABASE_AUTHORITY=test", nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	command.SetContext(ctx)
	runGatewayChild = func(
		context.Context, *cobra.Command, *database.Client, string, string, bool, bool, bool,
	) error {
		cancel()
		return errors.New("runtime exit after cancellation")
	}
	if err := runSupervisedGateway(command, false, false, false); err != nil {
		t.Fatalf("canceled supervised gateway error = %v", err)
	}
}

func TestRecoverGatewaySupervisorClassifiesReadinessAndStatusErrors(t *testing.T) {
	installGatewaySupervisorSeams(t)
	home := t.TempDir()
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home:           home,
		RequiredStores: []database.StoreID{"test.required"},
		StatusProvider: func(context.Context) ([]database.StoreStatus, error) {
			return []database.StoreStatus{{
				ID: "test.required", Readiness: database.StoreUnavailable,
				Error: database.NewError(database.CodeUnavailable, "not ready"),
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	ensureGatewaySupervisor = func(context.Context, database.EnsureOptions) (*database.Client, error) {
		return client, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	recovered, err := recoverGatewayDatabaseSupervisor(ctx, home, "/test/picoclaw")
	if recovered != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("not-ready recovery = client:%p err:%v", recovered, err)
	}
	closeGatewayTestServer(server)
	if err := requireGatewayDatabaseReadiness(t.Context(), client); err == nil {
		t.Fatal("closed broker status succeeded")
	}
}

func installGatewaySupervisorSeams(t *testing.T) {
	t.Helper()
	previousExecutable := resolveGatewayRuntimeExecutable
	previousEnsure := ensureGatewaySupervisor
	previousAuthority := gatewayInheritedAuthority
	previousChild := runGatewayChild
	previousCore := runGatewayCore
	t.Cleanup(func() {
		resolveGatewayRuntimeExecutable = previousExecutable
		ensureGatewaySupervisor = previousEnsure
		gatewayInheritedAuthority = previousAuthority
		runGatewayChild = previousChild
		runGatewayCore = previousCore
	})
}

func startGatewaySupervisorClient(t *testing.T, home string) (*database.Client, *database.Server) {
	t.Helper()
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home:           home,
		RequiredStores: []database.StoreID{"test.required"},
		StatusProvider: func(context.Context) ([]database.StoreStatus, error) {
			return []database.StoreStatus{{ID: "test.required", Readiness: database.StoreReady}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGatewayTestServer(server) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func writeGatewayChildScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime-child")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func connectGatewayTestClient(t *testing.T) *database.Client {
	t.Helper()
	home := t.TempDir()
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeGatewayTestServer(server) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func closeGatewayTestServer(server *database.Server) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Close(ctx)
}
