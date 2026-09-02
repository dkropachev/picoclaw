//go:build unix

package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

const supervisorProcessHelperEnvironment = "PICOCLAW_DATABASE_TEST_SUPERVISOR_HELPER"

func TestEnsureSupervisorRestartsChangedCatalogConfiguration(t *testing.T) {
	home := t.TempDir()
	configFile := filepath.Join(t.TempDir(), "config.json")
	configuration := config.DefaultConfig()
	configuration.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	if err := config.SaveConfig(configFile, configuration); err != nil {
		t.Fatal(err)
	}

	helper := filepath.Join(t.TempDir(), "supervisor-helper")
	script := fmt.Sprintf(
		"#!/bin/sh\nexec %q -test.run=^TestSupervisorProcessHelper$ -- \"$@\"\n",
		os.Args[0],
	)
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(supervisorProcessHelperEnvironment, "1")
	options := EnsureOptions{
		Home: home, Executable: helper, ConfigPath: configFile, Timeout: 10 * time.Second,
	}
	first, err := EnsureSupervisor(context.Background(), options)
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(home, "logs", "database-supervisor.log"))
		t.Fatalf("%v\nsupervisor log:\n%s", err, log)
	}
	t.Cleanup(func() {
		client, connectErr := Connect(home)
		if connectErr == nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = client.Shutdown(shutdownCtx)
			cancel()
		}
	})
	firstEpoch := first.Epoch()
	firstStatus, err := first.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	attached, err := EnsureSupervisor(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if attached.Epoch() != firstEpoch {
		t.Fatal("unchanged catalog configuration replaced the broker epoch")
	}

	configuration.Workflows.Enabled = !configuration.Workflows.Enabled
	if saveErr := config.SaveConfig(configFile, configuration); saveErr != nil {
		t.Fatal(saveErr)
	}
	second, err := EnsureSupervisor(context.Background(), options)
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(home, "logs", "database-supervisor.log"))
		t.Fatalf("%v\nsupervisor log:\n%s", err, log)
	}
	if second.Epoch() == firstEpoch {
		t.Fatal("changed catalog configuration retained the broker epoch")
	}
	secondStatus, err := second.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if secondStatus.CatalogFingerprint == firstStatus.CatalogFingerprint ||
		!validCatalogFingerprint(secondStatus.CatalogFingerprint) {
		t.Fatalf("replacement catalog fingerprint = %q", secondStatus.CatalogFingerprint)
	}
}

func TestSupervisorProcessHelper(t *testing.T) {
	if os.Getenv(supervisorProcessHelperEnvironment) != "1" {
		t.Skip("supervisor process helper")
	}
	home := helperFlagValue(os.Args, "--home")
	if home == "" || !ConsumeSupervisorBootstrap(home) {
		t.Fatal("helper did not receive valid supervisor authority")
	}
	_, fingerprint, err := LoadCatalogConfiguration(os.Getenv(config.EnvConfig))
	if err != nil {
		t.Fatal(err)
	}
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home, CatalogFingerprint: fingerprint,
		RequiredStores: []StoreID{"global.auth"},
		StatusProvider: func(context.Context) ([]StoreStatus, error) {
			return []StoreStatus{{ID: "global.auth", Readiness: StoreReady}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-server.Done()
}

func helperFlagValue(arguments []string, name string) string {
	for index, argument := range arguments {
		if argument == name && index+1 < len(arguments) {
			return strings.TrimSpace(arguments[index+1])
		}
	}
	return ""
}
