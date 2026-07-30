//go:build linux

package api

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverAgentCapabilitiesExchangeKeepsNewestConcurrentEdit(
	t *testing.T,
) {
	directory := t.TempDir()
	targetPath := filepath.Join(directory, "AGENT.md")
	temporaryPath := filepath.Join(directory, ".agent-capabilities-candidate")
	candidate := []byte("---\ntools: []\n---\ncandidate\n")
	displaced := []byte("---\ntools: [exec]\n---\ndisplaced\n")
	newest := []byte("---\ntools: [read_file]\n---\nnewest\n")
	if err := os.WriteFile(targetPath, candidate, 0o600); err != nil {
		t.Fatalf("WriteFile(candidate) error = %v", err)
	}
	if err := os.WriteFile(temporaryPath, displaced, 0o600); err != nil {
		t.Fatalf("WriteFile(displaced) error = %v", err)
	}
	candidateIdentity, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("Lstat(candidate) error = %v", err)
	}

	originalHook := agentCapabilitiesBeforeConflictRecoverySwap
	t.Cleanup(func() {
		agentCapabilitiesBeforeConflictRecoverySwap = originalHook
	})
	agentCapabilitiesBeforeConflictRecoverySwap = func() {
		agentCapabilitiesBeforeConflictRecoverySwap = func() {}
		replacementPath := filepath.Join(directory, ".newest")
		if writeErr := os.WriteFile(replacementPath, newest, 0o600); writeErr != nil {
			t.Fatalf("WriteFile(newest) error = %v", writeErr)
		}
		if renameErr := os.Rename(replacementPath, targetPath); renameErr != nil {
			t.Fatalf("Rename(newest) error = %v", renameErr)
		}
	}

	consumed, recoveryErr := recoverAgentCapabilitiesExchangeLinux(
		temporaryPath,
		targetPath,
		candidateIdentity,
		errAgentCapabilitiesRevisionMismatch,
	)
	if !consumed {
		t.Fatal("recoverAgentCapabilitiesExchangeLinux() did not consume temp")
	}
	if !errors.Is(recoveryErr, errAgentCapabilitiesRevisionMismatch) {
		t.Fatalf("recovery error = %v", recoveryErr)
	}
	current, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("ReadFile(target) error = %v", readErr)
	}
	if !bytes.Equal(current, newest) {
		t.Fatalf("target = %q, want newest edit %q", current, newest)
	}
	if _, statErr := os.Lstat(temporaryPath); !os.IsNotExist(statErr) {
		t.Fatalf("temporary backup remains after recovery: %v", statErr)
	}
}
