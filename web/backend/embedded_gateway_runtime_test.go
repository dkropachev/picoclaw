package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/web/backend/api"
)

func TestRunEmbeddedGatewayRuntimeHonorsHostCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	readyCalled := false

	err := runEmbeddedGatewayRuntime(ctx, api.EmbeddedGatewayRunOptions{
		Debug:               true,
		HomePath:            t.TempDir(),
		ConfigPath:          filepath.Join(t.TempDir(), "unused.json"),
		AllowEmptyStartup:   true,
		ManageLogLevel:      true,
		GatewayHostOverride: "127.0.0.2",
		OnReady: func(_ ppid.PidFileData) {
			readyCalled = true
		},
	})
	if err != nil {
		t.Fatalf("runEmbeddedGatewayRuntime(canceled) error = %v, want nil", err)
	}
	if readyCalled {
		t.Fatal("canceled embedded runtime published readiness")
	}
}

func reserveLauncherTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatalf("release test port: %v", err)
	}
	return port
}

func TestLauncherMainHostsAndShutsDownEmbeddedGateway(t *testing.T) {
	homePath := t.TempDir()
	t.Setenv("PICOCLAW_HOME", homePath)
	configPath := filepath.Join(homePath, "config.json")
	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = reserveLauncherTestPort(t)
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	originalFlags := flag.CommandLine
	originalArgs := os.Args
	originalServeErrors := launcherServeErrors
	originalServers := servers
	originalAPIHandler := apiHandler
	originalNoBrowser := noBrowser
	t.Cleanup(func() {
		flag.CommandLine = originalFlags
		os.Args = originalArgs
		launcherServeErrors = originalServeErrors
		servers = originalServers
		apiHandler = originalAPIHandler
		noBrowser = originalNoBrowser
	})

	launcherPort := reserveLauncherTestPort(t)
	flag.CommandLine = flag.NewFlagSet("launcher-main-integration", flag.ContinueOnError)
	os.Args = []string{
		"picoclaw-launcher-test",
		"-console",
		"-no-browser",
		"-host", "127.0.0.1",
		"-port", strconv.Itoa(launcherPort),
		configPath,
	}
	launcherServeErrors = make(chan error, 1)
	launcherServeErrors <- errors.New("test requested launcher shutdown")

	main()

	if apiHandler == nil {
		t.Fatal("launcher main did not configure an API handler")
	}
	if len(servers) != 1 {
		t.Fatalf("launcher server count = %d, want 1", len(servers))
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(launcherPort)))
	if err != nil {
		t.Fatalf("launcher listener was not released after shutdown: %v", err)
	}
	_ = listener.Close()
}
