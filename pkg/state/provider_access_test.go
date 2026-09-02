package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	// Legacy package tests exercise provider internals directly. Production
	// callers must use the runtime broker or hold an owner fence.
	allowUnfencedRuntimeStateProviderForTests.Store(true)
}

func TestRuntimeConstructorsWithoutBrokerFailClosed(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedRuntimeStateProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedRuntimeStateProviderForTests.Store(true) })
	previousClient := runtimeStateBrokerClient
	runtimeStateBrokerClient = func() *database.Client { return nil }
	t.Cleanup(func() { runtimeStateBrokerClient = previousClient })

	workspace := filepath.Join(t.TempDir(), "must-not-create")
	manager, err := NewSQLiteManager(workspace)
	if manager != nil || database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("unfenced runtime manager = %#v, error %v", manager, err)
	}
	compatibility := NewManager(workspace)
	if compatibility == nil || database.CodeOf(compatibility.brokerErr) != database.CodeUnavailable {
		t.Fatalf("unfenced compatibility manager = %#v", compatibility)
	}
	if err := compatibility.SetLastChannel("must-fail"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("unfenced compatibility mutation error = %v", err)
	}
	if _, err := newSQLiteManagerLocal(workspace); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced offline manager error = %v", err)
	}
	if _, err := newRetainedSQLiteManager(workspace); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced retained manager error = %v", err)
	}
	if _, err := openRuntimeStateLockFile(
		filepath.Join(workspace, runtimeDatabaseFilename),
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("unfenced state lock error = %v", err)
	}
	if _, err := NewBrokerHandler(t.TempDir(), nil); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced broker handler error = %v", err)
	}
	if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
		t.Fatalf("unfenced runtime constructor created workspace: %v", err)
	}
}
