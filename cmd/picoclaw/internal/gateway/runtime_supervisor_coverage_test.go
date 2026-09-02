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
