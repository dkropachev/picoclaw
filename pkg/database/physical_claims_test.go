package database

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestPhysicalClaimsRequireOwnerAuthority(t *testing.T) {
	restoreAuthority := SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	if claims, err := AcquireCatalogStoreClaims(t.TempDir(), &config.Config{}); claims != nil ||
		CodeOf(err) != CodeUnauthorized {
		t.Fatalf("unfenced physical claims = %#v, %v", claims, err)
	}
}

func TestPhysicalStoreClaimsFenceSameWorkspaceAcrossHomes(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared-workspace")
	firstHome, secondHome := t.TempDir(), t.TempDir()
	firstConfig := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: shared},
	}}
	secondConfig := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: shared},
	}}
	first, acquireErr := AcquireCatalogStoreClaims(firstHome, firstConfig)
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	defer first.Close()
	if _, err := AcquireCatalogStoreClaims(secondHome, secondConfig); CodeOf(err) != CodeConflict {
		t.Fatalf("second physical claim error = %v, want Conflict", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireCatalogStoreClaims(secondHome, secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
}
