package api

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAgentCapabilitiesConditionalReplaceRejectsLateExistingFileEdit(
	t *testing.T,
) {
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
	path := filepath.Join(workspace, agentDefinitionFileCurrent)
	initial := []byte("---\ntools: [exec]\n---\ninitial prompt\n")
	if err = os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatalf("WriteFile(initial) error = %v", err)
	}
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	external := []byte("---\ntools: [read_file]\n---\nlate external edit\n")
	previousHook := agentCapabilitiesBeforeConditionalReplace
	t.Cleanup(func() {
		agentCapabilitiesBeforeConditionalReplace = previousHook
	})
	agentCapabilitiesBeforeConditionalReplace = func() {
		agentCapabilitiesBeforeConditionalReplace = func() {}
		if writeErr := os.WriteFile(path, external, 0o600); writeErr != nil {
			t.Fatalf("WriteFile(external) error = %v", writeErr)
		}
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(
			recorder.Body.String(),
			"capabilities_revision_mismatch",
		) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	current, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(current, external) {
		t.Fatalf("late edit was overwritten: data=%q err=%v", current, readErr)
	}
}

func TestAgentCapabilitiesConditionalCreateRejectsLateFileCreation(
	t *testing.T,
) {
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
	path := filepath.Join(workspace, agentDefinitionFileCurrent)
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	external := []byte("# file created by another editor\n")
	previousHook := agentCapabilitiesBeforeConditionalReplace
	t.Cleanup(func() {
		agentCapabilitiesBeforeConditionalReplace = previousHook
	})
	agentCapabilitiesBeforeConditionalReplace = func() {
		agentCapabilitiesBeforeConditionalReplace = func() {}
		if writeErr := os.WriteFile(path, external, 0o600); writeErr != nil {
			t.Fatalf("WriteFile(external) error = %v", writeErr)
		}
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(
			recorder.Body.String(),
			"capabilities_revision_mismatch",
		) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	current, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(current, external) {
		t.Fatalf("late creation was overwritten: data=%q err=%v", current, readErr)
	}
}

func TestAgentCapabilitiesLegacyUpgradeRejectsPostCreateLegacyEdit(
	t *testing.T,
) {
	if !agentCapabilitiesConditionalCreateSupported() {
		t.Skip("platform does not support conditional capability creation")
	}
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
	legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
	legacy := []byte("# Original legacy prompt\n")
	if err = os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
	}
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	external := []byte("# Concurrent legacy prompt edit\n")
	previousHook := agentCapabilitiesAfterConditionalCreate
	t.Cleanup(func() {
		agentCapabilitiesAfterConditionalCreate = previousHook
	})
	agentCapabilitiesAfterConditionalCreate = func() {
		agentCapabilitiesAfterConditionalCreate = func() {}
		if writeErr := os.WriteFile(legacyPath, external, 0o600); writeErr != nil {
			t.Fatalf("WriteFile(concurrent AGENTS.md) error = %v", writeErr)
		}
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			UpgradeLegacy:    true,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(
			recorder.Body.String(),
			"capabilities_revision_mismatch",
		) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, statErr := os.Lstat(
		filepath.Join(workspace, agentDefinitionFileCurrent),
	); !os.IsNotExist(statErr) {
		t.Fatalf("stale AGENT.md remains after rollback: %v", statErr)
	}
	currentLegacy, readErr := os.ReadFile(legacyPath)
	if readErr != nil || !bytes.Equal(currentLegacy, external) {
		t.Fatalf(
			"concurrent legacy edit changed: data=%q err=%v",
			currentLegacy,
			readErr,
		)
	}
}

func TestAgentCapabilitiesLegacyRollbackPreservesIdenticalReplacement(
	t *testing.T,
) {
	if !agentCapabilitiesConditionalCreateSupported() {
		t.Skip("platform does not support conditional capability creation")
	}
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
	targetPath := filepath.Join(workspace, agentDefinitionFileCurrent)
	legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
	if err = os.WriteFile(
		legacyPath,
		[]byte("# Original legacy prompt\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
	}
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	previousHook := agentCapabilitiesAfterConditionalCreate
	t.Cleanup(func() {
		agentCapabilitiesAfterConditionalCreate = previousHook
	})
	var replacementIdentity fs.FileInfo
	agentCapabilitiesAfterConditionalCreate = func() {
		agentCapabilitiesAfterConditionalCreate = func() {}
		candidate, readErr := os.ReadFile(targetPath)
		if readErr != nil {
			t.Fatalf("ReadFile(generated AGENT.md) error = %v", readErr)
		}
		generatedInfo, readErr := os.Lstat(targetPath)
		if readErr != nil {
			t.Fatalf("Lstat(generated AGENT.md) error = %v", readErr)
		}
		replacementPath := filepath.Join(workspace, ".external-identical")
		if writeErr := os.WriteFile(
			replacementPath,
			candidate,
			generatedInfo.Mode().Perm(),
		); writeErr != nil {
			t.Fatalf("WriteFile(identical replacement) error = %v", writeErr)
		}
		if renameErr := os.Rename(
			replacementPath,
			targetPath,
		); renameErr != nil {
			t.Fatalf("Rename(identical replacement) error = %v", renameErr)
		}
		replacementIdentity, readErr = os.Lstat(targetPath)
		if readErr != nil {
			t.Fatalf("Lstat(identical replacement) error = %v", readErr)
		}
		if writeErr := os.WriteFile(
			legacyPath,
			[]byte("# Concurrent legacy edit\n"),
			0o600,
		); writeErr != nil {
			t.Fatalf("WriteFile(concurrent AGENTS.md) error = %v", writeErr)
		}
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			UpgradeLegacy:    true,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(
			recorder.Body.String(),
			"capabilities_revision_mismatch",
		) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	currentIdentity, statErr := os.Lstat(targetPath)
	if statErr != nil {
		t.Fatalf("identical external replacement was removed: %v", statErr)
	}
	if replacementIdentity == nil ||
		!os.SameFile(currentIdentity, replacementIdentity) {
		t.Fatal("identical external replacement identity changed during rollback")
	}
}

func TestAgentCapabilitiesCreationRejectsPostCreateLegacyAppearance(
	t *testing.T,
) {
	if !agentCapabilitiesConditionalCreateSupported() {
		t.Skip("platform does not support conditional capability creation")
	}
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
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
	external := []byte("# Concurrent legacy creation\n")
	previousHook := agentCapabilitiesAfterConditionalCreate
	t.Cleanup(func() {
		agentCapabilitiesAfterConditionalCreate = previousHook
	})
	agentCapabilitiesAfterConditionalCreate = func() {
		agentCapabilitiesAfterConditionalCreate = func() {}
		if writeErr := os.WriteFile(legacyPath, external, 0o600); writeErr != nil {
			t.Fatalf("WriteFile(concurrent AGENTS.md) error = %v", writeErr)
		}
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(
			recorder.Body.String(),
			"capabilities_revision_mismatch",
		) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, statErr := os.Lstat(
		filepath.Join(workspace, agentDefinitionFileCurrent),
	); !os.IsNotExist(statErr) {
		t.Fatalf("stale AGENT.md remains after rollback: %v", statErr)
	}
	currentLegacy, readErr := os.ReadFile(legacyPath)
	if readErr != nil || !bytes.Equal(currentLegacy, external) {
		t.Fatalf(
			"concurrent legacy creation changed: data=%q err=%v",
			currentLegacy,
			readErr,
		)
	}
}

func TestAgentCapabilitiesCreationErrorRollsBackVisibleCurrentFile(
	t *testing.T,
) {
	if !agentCapabilitiesConditionalCreateSupported() {
		t.Skip("platform does not support conditional capability creation")
	}
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
	legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
	legacy := []byte("# Original legacy prompt\n")
	if err = os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
	}
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	originalWriter := writeAgentCapabilitiesFile
	t.Cleanup(func() {
		writeAgentCapabilitiesFile = originalWriter
	})
	external := []byte("# Concurrent legacy edit during failed commit\n")
	writeAgentCapabilitiesFile = func(
		target string,
		data []byte,
		permission fs.FileMode,
		_ agentDefinitionFile,
		_ bool,
	) (agentCapabilitiesWriteResult, error) {
		writeAgentCapabilitiesFile = originalWriter
		if writeErr := os.WriteFile(target, data, permission); writeErr != nil {
			t.Fatalf("WriteFile(visible AGENT.md) error = %v", writeErr)
		}
		if writeErr := os.WriteFile(legacyPath, external, 0o600); writeErr != nil {
			t.Fatalf("WriteFile(concurrent AGENTS.md) error = %v", writeErr)
		}
		identity, statErr := os.Lstat(target)
		if statErr != nil {
			t.Fatalf("Lstat(visible AGENT.md) error = %v", statErr)
		}
		return agentCapabilitiesWriteResult{
				candidateIdentity: identity,
			}, &agentCapabilitiesVisibleCommitError{
				err: errors.New("simulated directory sync failure"),
			}
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			UpgradeLegacy:    true,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(recorder.Body.String(), "capabilities_save_failed") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, statErr := os.Lstat(
		filepath.Join(workspace, agentDefinitionFileCurrent),
	); !os.IsNotExist(statErr) {
		t.Fatalf("uncommitted AGENT.md remains after rollback: %v", statErr)
	}
	currentLegacy, readErr := os.ReadFile(legacyPath)
	if readErr != nil || !bytes.Equal(currentLegacy, external) {
		t.Fatalf(
			"concurrent legacy edit changed: data=%q err=%v",
			currentLegacy,
			readErr,
		)
	}
}

func TestAgentCapabilitiesConditionalCreatePreservesIdenticalExternalFile(
	t *testing.T,
) {
	if !agentCapabilitiesConditionalCreateSupported() {
		t.Skip("platform does not support conditional capability creation")
	}
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
	targetPath := filepath.Join(workspace, agentDefinitionFileCurrent)
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	originalWriter := writeAgentCapabilitiesFile
	t.Cleanup(func() {
		writeAgentCapabilitiesFile = originalWriter
	})
	var external []byte
	writeAgentCapabilitiesFile = func(
		target string,
		data []byte,
		permission fs.FileMode,
		expected agentDefinitionFile,
		expectedExists bool,
	) (agentCapabilitiesWriteResult, error) {
		writeAgentCapabilitiesFile = originalWriter
		external = append([]byte(nil), data...)
		if writeErr := os.WriteFile(target, data, permission); writeErr != nil {
			t.Fatalf("WriteFile(identical external AGENT.md) error = %v", writeErr)
		}
		return originalWriter(
			target,
			data,
			permission,
			expected,
			expectedExists,
		)
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(
			recorder.Body.String(),
			"capabilities_revision_mismatch",
		) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	current, readErr := os.ReadFile(targetPath)
	if readErr != nil || !bytes.Equal(current, external) {
		t.Fatalf(
			"identical external file was removed: data=%q err=%v",
			current,
			readErr,
		)
	}
}
