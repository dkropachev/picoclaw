package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionSourceProbeCloseoutCheckpointMatrix(t *testing.T) {
	injected := errors.New("injected source probe checkpoint")
	for failAt := 1; failAt <= 10; failAt++ {
		t.Run(string(rune('a'+failAt)), func(t *testing.T) {
			intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
			defer intent.Close()
			calls := 0
			err := probeApplyPatchTxnSourceFallbackCapabilities(
				context.Background(),
				intent,
				journal,
				func(*applyPatchTransactionJournal) error {
					calls++
					if calls == failAt {
						return injected
					}
					return nil
				},
				nil,
			)
			if calls < failAt {
				return
			}
			if !errors.Is(err, injected) {
				t.Fatalf("checkpoint %d = %v", failAt, err)
			}
		})
	}
}

func TestApplyPatchTransactionSourceProbeCloseoutDefensiveBranches(t *testing.T) {
	if err := probeApplyPatchTxnSourceFallbackCapabilities(nil, nil, nil, nil, nil); err == nil {
		t.Fatal("nil source probe state succeeded")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
	defer intent.Close()
	if err := probeApplyPatchTxnSourceFallbackCapabilities(
		canceled,
		intent,
		journal,
		func(*applyPatchTransactionJournal) error { return nil },
		nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source probe = %v", err)
	}
	if err := probeApplyPatchTxnOneSourceFallback(
		context.Background(), nil, journal,
		func(*applyPatchTransactionJournal) error { return nil }, nil,
	); err == nil {
		t.Fatal("nil source endpoint probe succeeded")
	}
	if err := runApplyPatchTxnSourceProbeFault(nil, "test", 0); err != nil {
		t.Fatalf("nil source fault = %v", err)
	}
	resetApplyPatchTxnSourceProbeArtifact(nil)
	if err := validateApplyPatchTxnPreEffectDeclaredNames(nil, nil); err == nil {
		t.Fatal("nil pre-effect names validated")
	}
	if err := validateApplyPatchTxnPreEffectRootedNames(nil); err == nil {
		t.Fatal("nil rooted names validated")
	}
	if err := validateApplyPatchTxnInactiveSourceProbeNames(nil, nil); err == nil {
		t.Fatal("nil inactive source probe validated")
	}

	op := intent.operations[0]
	op.source.anchor.identity.Device = 0
	if err := probeApplyPatchTxnSourceFallbackCapabilities(
		context.Background(), intent, journal,
		func(*applyPatchTransactionJournal) error { return nil }, nil,
	); err == nil {
		t.Fatal("zero-device source probe succeeded")
	}
}

func TestApplyPatchTransactionSourceProbeCloseoutDeclaredNameConflicts(t *testing.T) {
	intent, journal := newApplyPatchTxnSourceProbeCloseoutFixture(t)
	defer intent.Close()
	op := intent.operations[0]
	probe, err := requireApplyPatchTxnArtifact(
		journal, 0, applyPatchTransactionArtifactSourceProbeWitness,
	)
	if err != nil {
		t.Fatal(err)
	}
	probe.Rooted.RemovalAttempted = true
	if err := validateApplyPatchTxnPreEffectRootedNames(probe.Rooted); err == nil {
		t.Fatal("attempted source probe removal validated")
	}
	probe.Rooted.RemovalAttempted = false
	probe.Rooted.AnchorCanonicalPath = filepath.Join(t.TempDir(), "missing")
	if err := validateApplyPatchTxnPreEffectRootedNames(probe.Rooted); err == nil {
		t.Fatal("missing source probe anchor validated")
	}
	probe.Rooted.AnchorCanonicalPath = op.source.anchor.canonical
	probe.Rooted.AnchorIdentity = op.source.anchor.identity
	removal := filepath.Join(probe.Rooted.AnchorCanonicalPath, probe.Rooted.RemovalBasename)
	if err := os.WriteFile(removal, []byte("alien\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateApplyPatchTxnPreEffectRootedNames(probe.Rooted); err == nil {
		t.Fatal("present source probe removal validated")
	}
	if err := os.Remove(removal); err != nil {
		t.Fatal(err)
	}
	basename := filepath.Join(probe.Rooted.AnchorCanonicalPath, probe.Rooted.Basename)
	if err := os.WriteFile(basename, []byte("alien\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateApplyPatchTxnInactiveSourceProbeNames(op, journal); err == nil {
		t.Fatal("present inactive source probe validated")
	}
}

func newApplyPatchTxnSourceProbeCloseoutFixture(
	t *testing.T,
) (*applyPatchTxnIntentPlan, *applyPatchTransactionJournal) {
	t.Helper()
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "source\n", 0o640)
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
	)
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	key, workspaceBinding, stateBinding := applyPatchTxnTestBindings(
		t, plan.workspace, t.TempDir(), intent,
	)
	journal, err := newApplyPatchTxnPreparingJournal(
		key, workspaceBinding, stateBinding, intent,
	)
	if err != nil {
		_ = intent.Close()
		t.Fatal(err)
	}
	return intent, journal
}
