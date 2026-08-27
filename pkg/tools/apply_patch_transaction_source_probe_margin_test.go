//go:build linux

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionSourceProbeMarginStagedWitnessGuards(t *testing.T) {
	intent, journal := newApplyPatchTxnSourceProbeMarginUpdateFixture(t)
	defer intent.Close()
	checkpoint := func(*applyPatchTransactionJournal) error { return nil }
	if err := probeApplyPatchTxnSourceFallbackCapabilities(
		context.Background(), intent, journal, checkpoint, nil,
	); err == nil {
		t.Fatal("missing staged witness was accepted")
	}
	if _, err := applyPatchTxnStagedWitnessFilesystems(intent, journal); err == nil {
		t.Fatal("missing staged witness filesystem was accepted")
	}

	nilForestIntent := &applyPatchTxnIntentPlan{
		forests: []*applyPatchTxnForestIntent{nil},
	}
	if proven, err := applyPatchTxnStagedWitnessFilesystems(
		nilForestIntent,
		&applyPatchTransactionJournal{},
	); err != nil || len(proven) != 0 {
		t.Fatalf("nil forest result = %v, %v", proven, err)
	}

	forestIntent, forestJournal, _ := newApplyPatchTxnStageCloseoutFixture(
		t,
		"*** Begin Patch\n*** Add File: nested/result.txt\n+result\n*** End Patch",
	)
	defer forestIntent.Close()
	forestIntent.forests[0].operations[0].planned.kind = "move"
	if _, err := applyPatchTxnStagedWitnessFilesystems(forestIntent, forestJournal); err == nil {
		t.Fatal("missing forest staged witness was accepted")
	}
}

func TestApplyPatchTransactionSourceProbeMarginArtifactGuards(t *testing.T) {
	t.Run("missing stage", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		for index := range journal.Artifacts {
			if journal.Artifacts[index].Role == applyPatchTransactionArtifactSourceRestoreStage {
				journal.Artifacts = append(journal.Artifacts[:index], journal.Artifacts[index+1:]...)
				break
			}
		}
		if err := probeApplyPatchTxnOneSourceFallback(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil }, nil,
		); err == nil {
			t.Fatal("missing source stage was accepted")
		}
	})
	t.Run("missing witness", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		for index := range journal.Artifacts {
			if journal.Artifacts[index].Role == applyPatchTransactionArtifactSourceProbeWitness {
				journal.Artifacts = append(journal.Artifacts[:index], journal.Artifacts[index+1:]...)
				break
			}
		}
		if err := probeApplyPatchTxnOneSourceFallback(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil }, nil,
		); err == nil {
			t.Fatal("missing source witness was accepted")
		}
	})
	t.Run("already active", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		stage, err := requireApplyPatchTxnArtifact(
			journal, 0, applyPatchTransactionArtifactSourceRestoreStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		stage.Rooted.Identity = &applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"}
		if err := probeApplyPatchTxnOneSourceFallback(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil }, nil,
		); err == nil {
			t.Fatal("active source probe was accepted")
		}
	})
	t.Run("changed source", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		intent.operations[0].source.state.Identity.File++
		if err := probeApplyPatchTxnOneSourceFallback(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil }, nil,
		); err == nil {
			t.Fatal("changed source was accepted")
		}
	})
	t.Run("occupied stage", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		stage, err := requireApplyPatchTxnArtifact(
			journal, 0, applyPatchTransactionArtifactSourceRestoreStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.Basename)
		if err := os.WriteFile(path, []byte("occupied\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := probeApplyPatchTxnOneSourceFallback(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil }, nil,
		); err == nil {
			t.Fatal("occupied source stage was accepted")
		}
	})
}

func TestApplyPatchTransactionSourceProbeMarginIOGuards(t *testing.T) {
	t.Run("canceled write", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := probeApplyPatchTxnOneSourceFallback(
			ctx, intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil }, nil,
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled write error = %v", err)
		}
	})
	t.Run("post-write sync", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		calls := 0
		err := probeApplyPatchTxnOneSourceFallback(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 1 {
					return intent.operations[0].source.anchor.Close()
				}
				return nil
			}, nil,
		)
		if err == nil {
			t.Fatal("closed source anchor was accepted after write")
		}
	})
	t.Run("stage identity changed", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		op := intent.operations[0]
		stage, err := requireApplyPatchTxnArtifact(
			journal, 0, applyPatchTransactionArtifactSourceRestoreStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		stagePath := filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.Basename)
		err = probeApplyPatchTxnOneSourceFallback(
			context.Background(), op, journal,
			func(*applyPatchTransactionJournal) error { return nil },
			func(boundary string) error {
				if boundary != "source_fallback_probe_created:0" {
					return nil
				}
				if renameErr := os.Rename(stagePath, stagePath+".old"); renameErr != nil {
					return renameErr
				}
				if writeErr := os.WriteFile(
					stagePath,
					op.planned.before,
					op.planned.mode.Perm(),
				); writeErr != nil {
					return writeErr
				}
				return os.Chmod(stagePath, op.planned.mode.Perm())
			},
		)
		if err == nil {
			t.Fatal("replaced source stage was accepted")
		}
	})
	t.Run("occupied witness", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		witness, err := requireApplyPatchTxnArtifact(
			journal, 0, applyPatchTransactionArtifactSourceProbeWitness,
		)
		if err != nil {
			t.Fatal(err)
		}
		witnessPath := filepath.Join(witness.Rooted.AnchorCanonicalPath, witness.Rooted.Basename)
		err = probeApplyPatchTxnOneSourceFallback(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
			func(boundary string) error {
				if boundary == "source_fallback_probe_written:0" {
					return os.WriteFile(witnessPath, []byte("occupied\n"), 0o600)
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("occupied source witness was accepted")
		}
	})
}

func TestApplyPatchTransactionSourceProbeMarginDeclaredNameGuards(t *testing.T) {
	newForest := func(t *testing.T) (*applyPatchTxnIntentPlan, *applyPatchTransactionJournal) {
		t.Helper()
		intent, journal, _ := newApplyPatchTxnStageCloseoutFixture(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+result\n*** End Patch",
		)
		return intent, journal
	}

	t.Run("missing forest", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		intent.forests[0].id = "missing"
		if err := validateApplyPatchTxnPreEffectDeclaredNames(intent, journal); err == nil {
			t.Fatal("missing declared forest was accepted")
		}
	})
	t.Run("invalid forest rooted state", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		journal.Forests[0].StageRoot.RemovalAttempted = true
		if err := validateApplyPatchTxnPreEffectDeclaredNames(intent, journal); err == nil {
			t.Fatal("invalid forest rooted state was accepted")
		}
	})
	t.Run("forest entry removal attempted", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		journal.Forests[0].Entries[1].RemovalAttempted = true
		if err := validateApplyPatchTxnPreEffectDeclaredNames(intent, journal); err == nil {
			t.Fatal("attempted forest entry removal was accepted")
		}
	})
	t.Run("forest stage parent absent", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		if err := validateApplyPatchTxnPreEffectDeclaredNames(intent, journal); err == nil {
			t.Fatal("absent forest staging parent was accepted")
		}
	})
	t.Run("anchor identity changed", func(t *testing.T) {
		intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
		defer intent.Close()
		artifact, err := requireApplyPatchTxnArtifact(
			journal, 0, applyPatchTransactionArtifactSourceProbeWitness,
		)
		if err != nil {
			t.Fatal(err)
		}
		artifact.Rooted.AnchorIdentity.File++
		if err := validateApplyPatchTxnPreEffectRootedNames(artifact.Rooted); err == nil {
			t.Fatal("changed artifact anchor was accepted")
		}
	})
}

func newApplyPatchTxnSourceProbeMarginUpdateFixture(
	t *testing.T,
) (*applyPatchTxnIntentPlan, *applyPatchTransactionJournal) {
	t.Helper()
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o640)
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
	)
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	key, workspaceBinding, stateBinding := applyPatchTxnTestBindings(
		t, plan.workspace, t.TempDir(), intent,
	)
	journal, err := newApplyPatchTxnPreparingJournal(key, workspaceBinding, stateBinding, intent)
	if err != nil {
		_ = intent.Close()
		t.Fatal(err)
	}
	return intent, journal
}
