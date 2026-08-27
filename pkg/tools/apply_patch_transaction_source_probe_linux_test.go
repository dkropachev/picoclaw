//go:build linux

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestApplyPatchTransactionSourceFallbackProbeFailsBeforePONRAndCleans(
	t *testing.T,
) {
	for _, boundary := range []string{"created", "written", "linked", "cleaned"} {
		t.Run(boundary, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "delete.txt", "source bytes\n", 0o751)
			before := applyPatchSnapshotTree(t, workspace)
			stateRoot := filepath.Join(t.TempDir(), "transaction-state")
			tool := newApplyPatchPreflightTestTool(
				t,
				workspace,
				true,
				true,
				ApplyPatchPreflightPolicy{TransactionStateRoot: stateRoot},
			)
			reachedPONR := false
			tool.afterPointOfNoReturn = func(*applyPatchPlan) { reachedPONR = true }
			injected := errors.New("injected source fallback probe failure")
			wanted := "source_fallback_probe_" + boundary + ":0"
			faultHit := false
			tool.transactionProbeFault = func(observed string) error {
				if observed != wanted {
					return nil
				}
				faultHit = true
				if boundary == "linked" {
					assertApplyPatchTxnSourceProbeLinkedState(t, workspace)
				}
				return injected
			}

			result := executeApplyPatch(
				t,
				tool,
				context.Background(),
				"*** Begin Patch\n*** Delete File: delete.txt\n*** End Patch",
			)
			if !faultHit {
				t.Fatalf("source fallback probe did not reach %q", wanted)
			}
			if result == nil || !result.IsError || result.ForUser != "" {
				t.Fatalf("source fallback probe result = %#v", result)
			}
			if reachedPONR {
				t.Fatal("source fallback capability failure crossed PONR")
			}
			assertApplyPatchTreeEqual(t, workspace, before)
			assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
			assertApplyPatchTxnFaultStateReady(t, workspace, stateRoot)
		})
	}
}

func TestApplyPatchTransactionSourceFallbackProbeDeduplicatesAndSkipsProven(
	t *testing.T,
) {
	t.Run("two deletes one filesystem", func(t *testing.T) {
		workspace := t.TempDir()
		writeApplyPatchFixture(t, workspace, "one.txt", "one\n", 0o600)
		writeApplyPatchFixture(t, workspace, "two.txt", "two\n", 0o640)
		tool := newApplyPatchPreflightTestTool(
			t,
			workspace,
			true,
			true,
			ApplyPatchPreflightPolicy{
				TransactionStateRoot: filepath.Join(t.TempDir(), "transaction-state"),
			},
		)
		var boundaries []string
		tool.transactionProbeFault = func(boundary string) error {
			boundaries = append(boundaries, boundary)
			return nil
		}
		result := executeApplyPatch(
			t,
			tool,
			context.Background(),
			"*** Begin Patch\n"+
				"*** Delete File: one.txt\n"+
				"*** Delete File: two.txt\n"+
				"*** End Patch",
		)
		if result.IsError {
			t.Fatalf("deduplicated source probe failed: %s", result.ForLLM)
		}
		sort.Strings(boundaries)
		want := []string{
			"source_fallback_probe_cleaned:0",
			"source_fallback_probe_created:0",
			"source_fallback_probe_linked:0",
			"source_fallback_probe_written:0",
		}
		if strings.Join(boundaries, "\n") != strings.Join(want, "\n") {
			t.Fatalf("probe boundaries = %q, want %q", boundaries, want)
		}
		assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
	})

	t.Run("update staged witness proves filesystem", func(t *testing.T) {
		workspace := t.TempDir()
		writeApplyPatchFixture(t, workspace, "update.txt", "before\n", 0o640)
		tool := newApplyPatchPreflightTestTool(
			t,
			workspace,
			true,
			true,
			ApplyPatchPreflightPolicy{
				TransactionStateRoot: filepath.Join(t.TempDir(), "transaction-state"),
			},
		)
		tool.transactionProbeFault = func(boundary string) error {
			t.Fatalf("already-proven filesystem was probed at %q", boundary)
			return nil
		}
		result := executeApplyPatch(
			t,
			tool,
			context.Background(),
			"*** Begin Patch\n"+
				"*** Update File: update.txt\n@@\n-before\n+after\n"+
				"*** End Patch",
		)
		if result.IsError {
			t.Fatalf("proven-filesystem update failed: %s", result.ForLLM)
		}
		assertApplyPatchTxnTestFile(
			t,
			filepath.Join(workspace, "update.txt"),
			"after\n",
			0o640,
		)
		assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
	})
}

func TestApplyPatchTransactionCrossFilesystemMoveProbesSourceBeforePONR(
	t *testing.T,
) {
	workspace := t.TempDir()
	external, err := os.MkdirTemp("/dev/shm", "picoclaw-source-probe-")
	if err != nil {
		t.Skipf("distinct temporary filesystem unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(external) })
	workspaceInfo, err := os.Lstat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	externalInfo, err := os.Lstat(external)
	if err != nil {
		t.Fatal(err)
	}
	workspaceIdentity, err := applyPatchTxnIdentityFromFileInfo(workspaceInfo, "directory")
	if err != nil {
		t.Fatal(err)
	}
	externalIdentity, err := applyPatchTxnIdentityFromFileInfo(externalInfo, "directory")
	if err != nil {
		t.Fatal(err)
	}
	if workspaceIdentity.Device == externalIdentity.Device {
		t.Skip("/dev/shm is not a distinct filesystem")
	}
	writeApplyPatchFixture(t, workspace, "source.txt", "cross filesystem\n", 0o751)
	if symlinkErr := os.Symlink(external, filepath.Join(workspace, "external")); symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	before := applyPatchSnapshotTree(t, workspace)
	stateRoot := filepath.Join(t.TempDir(), "transaction-state")
	tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		false,
		true,
		true,
		ApplyPatchPreflightPolicy{TransactionStateRoot: stateRoot},
	)
	if err != nil {
		t.Fatal(err)
	}
	reachedPONR := false
	tool.afterPointOfNoReturn = func(*applyPatchPlan) { reachedPONR = true }
	faultHit := false
	tool.transactionProbeFault = func(boundary string) error {
		if boundary == "source_fallback_probe_linked:0" {
			faultHit = true
			return errors.New("injected cross-filesystem source probe failure")
		}
		return nil
	}
	result := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n"+
			"*** Update File: source.txt\n*** Move to: external/target.txt\n"+
			"*** End Patch",
	)
	if !faultHit {
		t.Fatalf("cross-filesystem source probe was not reached: %#v", result)
	}
	if result == nil || !result.IsError || result.ForUser != "" {
		t.Fatalf("cross-filesystem probe result = %#v", result)
	}
	if reachedPONR {
		t.Fatal("cross-filesystem source probe failure crossed PONR")
	}
	assertApplyPatchTreeEqual(t, workspace, before)
	entries, err := os.ReadDir(external)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cross-filesystem target residue = %v, %v", entries, err)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
	assertApplyPatchTxnFaultStateReady(t, workspace, stateRoot)
}

func assertApplyPatchTxnSourceProbeLinkedState(t *testing.T, workspace string) {
	t.Helper()
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var stagePath, witnessPath string
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry.Name(), ".picoclaw-apply-patch-source-restore-"):
			stagePath = filepath.Join(workspace, entry.Name())
		case strings.HasPrefix(entry.Name(), ".picoclaw-apply-patch-source-probe-witness-"):
			witnessPath = filepath.Join(workspace, entry.Name())
		}
	}
	if stagePath == "" || witnessPath == "" {
		t.Fatalf("source probe stage/witness missing: %q / %q", stagePath, witnessPath)
	}
	stageInfo, err := os.Lstat(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	witnessInfo, err := os.Lstat(witnessPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(stageInfo, witnessInfo) || stageInfo.Mode().Perm() != 0o751 {
		t.Fatalf("source probe identities/mode = %v/%#o", os.SameFile(stageInfo, witnessInfo), stageInfo.Mode().Perm())
	}
	file, err := os.Open(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	links, linkErr := applyPatchLinkCount(file, stageInfo)
	closeErr := file.Close()
	if linkErr != nil || closeErr != nil || links != 2 {
		t.Fatalf("source probe link count = %d, %v", links, errors.Join(linkErr, closeErr))
	}
	data, err := os.ReadFile(stagePath)
	if err != nil || string(data) != "source bytes\n" {
		t.Fatalf("source probe content = %q, %v", data, err)
	}
}
