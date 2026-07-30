package api

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestGatewayRuntimeSignatureTracksIdentityOnlyAgentDefinition(t *testing.T) {
	cfg := config.DefaultConfig()
	workspace := t.TempDir()
	cfg.Agents.Defaults.Workspace = workspace
	path := filepath.Join(workspace, agentDefinitionFileCurrent)
	configSignature := computeConfigSignature(cfg)

	write := func(content string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(AGENT.md) error = %v", err)
		}
		return computeGatewayRuntimeSignature(cfg)
	}

	first := write("---\nname: First\n---\nordinary prose\n")
	if first == configSignature {
		t.Fatal("identity-only definition was omitted from runtime signature")
	}
	second := write("---\nname: Second\n---\nordinary prose\n")
	if second == first {
		t.Fatal("identity-only change did not affect runtime signature")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(AGENT.md) error = %v", err)
	}
	if got := computeGatewayRuntimeSignature(cfg); got != configSignature {
		t.Fatalf("identity removal signature = %q, want %q", got, configSignature)
	}
}

func TestGatewayRuntimeSignatureTracksTasksButNotOrdinaryPromptProse(
	t *testing.T,
) {
	cfg := config.DefaultConfig()
	workspace := t.TempDir()
	cfg.Agents.Defaults.Workspace = workspace
	path := filepath.Join(workspace, agentDefinitionFileCurrent)
	configSignature := computeConfigSignature(cfg)

	write := func(content string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(AGENT.md) error = %v", err)
		}
		return computeGatewayRuntimeSignature(cfg)
	}

	first := write("First ordinary paragraph.\n\n# Tasks\n- inspect events\n")
	if first == configSignature {
		t.Fatal("structured Tasks section was omitted from runtime signature")
	}
	proseChanged := write(
		"Completely different ordinary paragraph.\n\n# Tasks\n- inspect events\n",
	)
	if proseChanged != first {
		t.Fatal("ordinary prompt prose affected runtime signature")
	}
	taskChanged := write(
		"Completely different ordinary paragraph.\n\n# Tasks\n- dispatch workflow\n",
	)
	if taskChanged == proseChanged {
		t.Fatal("structured task change did not affect runtime signature")
	}
	ordinaryOnly := write("Only hot-reloaded ordinary prompt prose.\n")
	if ordinaryOnly != configSignature {
		t.Fatalf(
			"ordinary-only signature = %q, want %q",
			ordinaryOnly,
			configSignature,
		)
	}
}

func TestSemanticAgentDefinitionSignatureOmitsTasksForMalformedClosedFrontmatter(
	t *testing.T,
) {
	tests := map[string][]byte{
		"syntax error": []byte(`---
tools: [
---
# Tasks
- must not affect the runtime signature
`),
		"typed decode error": []byte(`---
tools: exec
---
# Tasks
- must not affect the runtime signature
`),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			entry, relevant := semanticAgentDefinitionSignature("main", data)
			if !relevant ||
				entry.State != malformedAgentFrontmatterSignatureState {
				t.Fatalf("signature entry = %#v, relevant = %v", entry, relevant)
			}
			if len(entry.Tasks) != 0 {
				t.Fatalf("signature tasks = %#v, want none", entry.Tasks)
			}
		})
	}
}

func TestGatewayRuntimeSignatureTracksUnsafeLegacyDefinitionTransitions(
	t *testing.T,
) {
	cfg := config.DefaultConfig()
	workspace := t.TempDir()
	cfg.Agents.Defaults.Workspace = workspace
	legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
	configSignature := computeConfigSignature(cfg)

	writeSafe := func(content string) string {
		t.Helper()
		if err := os.WriteFile(legacyPath, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
		}
		return computeGatewayRuntimeSignature(cfg)
	}

	safe := writeSafe("legacy prompt prose remains hot-reloadable\n")
	if safe != configSignature {
		t.Fatalf("safe legacy signature = %q, want %q", safe, configSignature)
	}
	if err := os.WriteFile(
		legacyPath,
		bytes.Repeat([]byte("x"), agentDefinitionMaxBytes+1),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(oversized AGENTS.md) error = %v", err)
	}
	oversized := computeGatewayRuntimeSignature(cfg)
	if oversized == safe ||
		oversized == gatewayUnknownBootConfigSignature {
		t.Fatalf("oversized legacy signature = %q, safe = %q", oversized, safe)
	}
	restored := writeSafe("different safe legacy prompt prose\n")
	if restored != safe {
		t.Fatalf("restored safe signature = %q, want %q", restored, safe)
	}

	if err := os.Remove(legacyPath); err != nil {
		t.Fatalf("Remove(AGENTS.md) error = %v", err)
	}
	targetPath := filepath.Join(workspace, "legacy-target.md")
	if err := os.WriteFile(targetPath, []byte("target prompt\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy target) error = %v", err)
	}
	if err := os.Symlink(targetPath, legacyPath); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	symlink := computeGatewayRuntimeSignature(cfg)
	if symlink == safe || symlink == gatewayUnknownBootConfigSignature {
		t.Fatalf("symlink legacy signature = %q, safe = %q", symlink, safe)
	}
	if symlink == oversized {
		t.Fatal("distinct unsafe legacy states produced the same signature")
	}
	if err := os.Remove(legacyPath); err != nil {
		t.Fatalf("Remove(AGENTS.md symlink) error = %v", err)
	}
	if final := writeSafe("safe again\n"); final != safe {
		t.Fatalf("safe signature after symlink = %q, want %q", final, safe)
	}
}

func TestGatewayRuntimeSignatureBoundsSafeLegacyDefinitions(t *testing.T) {
	cfg := config.DefaultConfig()
	root := t.TempDir()
	count := agentDefinitionSignatureByteLimit/agentDefinitionMaxBytes + 1
	cfg.Agents.List = make([]config.AgentConfig, count)
	content := bytes.Repeat([]byte("x"), agentDefinitionMaxBytes)
	for index := range cfg.Agents.List {
		workspace := filepath.Join(root, fmt.Sprintf("workspace-%03d", index))
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatalf("MkdirAll(workspace) error = %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(workspace, agentDefinitionFileLegacy),
			content,
			0o600,
		); err != nil {
			t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
		}
		cfg.Agents.List[index] = config.AgentConfig{
			ID:        fmt.Sprintf("agent-%03d", index),
			Workspace: workspace,
		}
	}
	if signature := computeAgentDefinitionsRuntimeSignature(cfg); signature != gatewayUnknownBootConfigSignature {
		t.Fatalf("aggregate legacy signature = %q, want unknown", signature)
	}
}
