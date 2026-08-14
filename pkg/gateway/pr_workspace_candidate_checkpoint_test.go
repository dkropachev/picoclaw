package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

func TestPRWorkspaceCandidateCheckpointStoreRoundTripAndRemove(t *testing.T) {
	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	if err = store.Save(checkpoint); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Lstat(store.path(checkpoint.WorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("checkpoint mode = %v", info.Mode())
	}
	loaded, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found || loaded != checkpoint {
		t.Fatalf("Load() = %#v, %v, %v", loaded, found, err)
	}
	if err = store.Remove(checkpoint.WorkspaceID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, found, err = store.Load(checkpoint.WorkspaceID); err != nil || found {
		t.Fatalf("Load(after remove) found = %v, error = %v", found, err)
	}
	if err = store.Remove(checkpoint.WorkspaceID); err != nil {
		t.Fatalf("idempotent Remove() error = %v", err)
	}
}

func TestPRWorkspaceCandidateCheckpointStoreRejectsUnsafeOrMalformedFiles(t *testing.T) {
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	t.Run("unknown field", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded[:len(encoded)-1], []byte(`,"unexpected":true}`)...)
		if err = os.WriteFile(store.path(checkpoint.WorkspaceID), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.Load(checkpoint.WorkspaceID); err == nil {
			t.Fatal("Load() accepted an unknown field")
		}
	})

	t.Run("public mode", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(checkpoint)
		if err = os.WriteFile(store.path(checkpoint.WorkspaceID), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.Load(checkpoint.WorkspaceID); err == nil {
			t.Fatal("Load() accepted a group/world-readable checkpoint")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.json")
		encoded, _ := json.Marshal(checkpoint)
		if err = os.WriteFile(target, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(target, store.path(checkpoint.WorkspaceID)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, err = store.Load(checkpoint.WorkspaceID); err == nil {
			t.Fatal("Load() accepted a symlink checkpoint")
		}
	})
}

func TestPRWorkspaceCandidateCheckpointRestoresExactCandidateAfterManagerRestart(t *testing.T) {
	ctx := context.Background()
	source := initPRWorkspaceRepairTestRepository(t)
	head := runPRWorkspaceRepairGit(t, source, "rev-parse", "HEAD")
	root := t.TempDir()
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := "prw_22222222222222222222222222222222"
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: source, SourceRef: "main", ExpectedCommit: head,
		ReservationKey: "pr-workspace:" + workspaceID, AgentID: "repair-test",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := manager.SnapshotPinnedValidationCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	lineID := stablePRWorkspaceLineID(workspaceID)
	lease, err := manager.AdoptPinnedLine(ctx, gitworkspace.PinnedLineAdoptRequest{
		Pin: pin, WorkspaceID: workspace.ID, LineID: lineID, ExpectedTree: baseline.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	charter := prworkspace.Charter{
		ID: "pchar_22222222222222222222222222222222", HeadSHA: head,
		Confirmed: true, CreatedAt: time.Now().UTC(),
	}
	store, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(manager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	active := prWorkspaceCandidate{pin: pin, candidate: baseline, charter: charter, lineID: lineID, lease: lease}
	runtime := &prWorkspaceImplementationRuntime{
		manager: manager, checkpoints: store,
		candidates: make(map[prWorkspaceCandidateKey]prWorkspaceCandidate),
		active:     map[string]prWorkspaceCandidate{workspaceID: active},
	}
	if err = runtime.saveCandidateCheckpoint(workspaceID, active); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace.Path, "README.md"), []byte("# retained repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = runtime.capturePartialRepairCandidate(ctx, workspaceID, active); err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.SnapshotPinnedCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	restartedManager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	restartedWorkspace, err := restartedManager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	restartedStore, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(restartedManager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &prWorkspaceImplementationRuntime{
		manager: restartedManager, checkpoints: restartedStore,
		candidates: make(map[prWorkspaceCandidateKey]prWorkspaceCandidate), active: make(map[string]prWorkspaceCandidate),
	}
	restored, found, err := restarted.restoreCheckpointedCandidate(
		ctx, pin, restartedWorkspace.ID, lineID, charter,
	)
	if err != nil || !found || restored.candidate != candidate || restored.lease != lease {
		t.Fatalf("restoreCheckpointedCandidate() = %#v, %v, %v", restored, found, err)
	}
	if _, ok := restarted.candidates[prWorkspaceCandidateKey{workspaceID: workspaceID, tree: candidate.Tree}]; !ok {
		t.Fatal("restored candidate was not available to validation")
	}

	if err = os.WriteFile(filepath.Join(workspace.Path, "README.md"), []byte("# out-of-band drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drifted := &prWorkspaceImplementationRuntime{
		manager: restartedManager, checkpoints: restartedStore,
		candidates: make(map[prWorkspaceCandidateKey]prWorkspaceCandidate), active: make(map[string]prWorkspaceCandidate),
	}
	if _, _, err = drifted.restoreCheckpointedCandidate(ctx, pin, restartedWorkspace.ID, lineID, charter); err == nil {
		t.Fatal("restoreCheckpointedCandidate() accepted content drift")
	}
}

func TestPRWorkspaceFinalizationRetainsCheckpointUntilAggregateAcknowledgement(t *testing.T) {
	ctx := context.Background()
	source := initPRWorkspaceRepairTestRepository(t)
	head := runPRWorkspaceRepairGit(t, source, "rev-parse", "HEAD")
	root := t.TempDir()
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := "prw_44444444444444444444444444444444"
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: source, SourceRef: "main", ExpectedCommit: head,
		ReservationKey: "pr-workspace:" + workspaceID, AgentID: "repair-test",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := manager.SnapshotPinnedValidationCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	lineID := stablePRWorkspaceLineID(workspaceID)
	lease, err := manager.AdoptPinnedLine(ctx, gitworkspace.PinnedLineAdoptRequest{
		Pin: pin, WorkspaceID: workspace.ID, LineID: lineID, ExpectedTree: baseline.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace.Path, "README.md"), []byte("# finalized repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.SnapshotPinnedCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	charter := prworkspace.Charter{
		ID: "pchar_44444444444444444444444444444444", HeadSHA: head,
		Confirmed: true, CreatedAt: time.Date(2026, 8, 14, 12, 0, 0, 987654321, time.UTC),
	}
	store, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(manager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	stored := prWorkspaceCandidate{pin: pin, candidate: candidate, charter: charter, lineID: lineID, lease: lease}
	runtime := &prWorkspaceImplementationRuntime{
		manager: manager, checkpoints: store,
		candidates: map[prWorkspaceCandidateKey]prWorkspaceCandidate{
			{workspaceID: workspaceID, tree: candidate.Tree}: stored,
		},
		active: map[string]prWorkspaceCandidate{workspaceID: stored},
	}
	if err = runtime.saveCandidateCheckpoint(workspaceID, stored); err != nil {
		t.Fatal(err)
	}
	repair := prworkspace.RepairResult{
		WorkspaceID: workspace.ID, CandidateSHA: candidate.Tree, Summary: "finalized",
	}
	if _, err = runtime.FinalizeRepair(ctx, "prw_55555555555555555555555555555555", repair); err == nil {
		t.Fatal("FinalizeRepair() accepted another PR workspace identity")
	}
	finalized, err := runtime.FinalizeRepair(ctx, workspaceID, repair)
	if err != nil || finalized.PublicationFence == nil || finalized.CandidateSHA == candidate.Tree {
		t.Fatalf("FinalizeRepair() = %#v, %v", finalized, err)
	}
	checkpoint, found, loadErr := store.Load(workspaceID)
	if loadErr != nil || !found || checkpoint.State != prWorkspaceCandidateCheckpointParked || checkpoint.Fence == nil {
		t.Fatalf("checkpoint was removed before aggregate acknowledgement: found=%v err=%v", found, loadErr)
	}
	restartedManager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	restartedStore, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(restartedManager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &prWorkspaceImplementationRuntime{manager: restartedManager, checkpoints: restartedStore}
	if _, _, restoreErr := restarted.restoreCheckpointedCandidate(
		ctx, pin, workspace.ID, lineID, charter,
	); restoreErr == nil || !strings.Contains(restoreErr.Error(), "awaits aggregate reconciliation") {
		t.Fatalf("parked checkpoint restore error = %v", restoreErr)
	}
	if err = restarted.AcknowledgeFinalizedRepair(ctx, workspaceID, finalized); err != nil {
		t.Fatal(err)
	}
	if _, found, loadErr := restartedStore.Load(workspaceID); loadErr != nil || found {
		t.Fatalf("acknowledged checkpoint remains: found=%v err=%v", found, loadErr)
	}
	if err = restarted.AcknowledgeFinalizedRepair(ctx, workspaceID, finalized); err != nil {
		t.Fatalf("AcknowledgeFinalizedRepair(replay) error = %v", err)
	}
}

func prWorkspaceCandidateCheckpointFixture() prWorkspaceCandidateCheckpoint {
	return prWorkspaceCandidateCheckpoint{
		Version:        prWorkspaceCandidateCheckpointVersion,
		State:          prWorkspaceCandidateCheckpointActive,
		WorkspaceID:    "prw_11111111111111111111111111111111",
		Repository:     "git@example.invalid:owner/repository.git",
		SourceRef:      "feature/checkpoint",
		HeadSHA:        "1111111111111111111111111111111111111111",
		CharterID:      "pchar_11111111111111111111111111111111",
		CharterHeadSHA: "1111111111111111111111111111111111111111",
		GitWorkspaceID: "gw-111111111111",
		LineID:         "pdln_11111111111111111111111111111111",
		Lease: gitworkspace.PinnedLineLease{
			WorkspaceID: "gw-111111111111", Version: 0, MutationEpoch: 1,
			Tip: "1111111111111111111111111111111111111111", Tree: "2222222222222222222222222222222222222222",
		},
		Candidate: gitworkspace.PinnedCandidate{
			WorkspaceID: "gw-111111111111", ParentCommit: "1111111111111111111111111111111111111111",
			Tree: "3333333333333333333333333333333333333333", CandidateDigest: "4444444444444444444444444444444444444444444444444444444444444444",
			ChangedFiles: 2,
		},
	}
}
