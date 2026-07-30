//go:build unix

package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAgentCapabilitiesFIFOIsRejectedWithoutBlocking(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, nil)
	cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	workspace := cfg.Agents.Defaults.Workspace
	if err = os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	if err = unix.Mkfifo(
		filepath.Join(workspace, agentDefinitionFileCurrent),
		0o600,
	); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}
	if err = os.WriteFile(
		filepath.Join(workspace, agentDefinitionFileLegacy),
		[]byte("must not fall back"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(legacy) error = %v", err)
	}

	response := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))
	if response.Source != "agent" || response.Editable ||
		response.IssueCode != "agent_definition_not_regular" ||
		response.LegacyUpgradeRequired {
		t.Fatalf("FIFO response = %#v", response)
	}
}
