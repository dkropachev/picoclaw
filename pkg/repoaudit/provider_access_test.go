package repoaudit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	// Legacy package tests exercise provider internals directly. Production
	// callers must use the runtime broker or hold an owner fence.
	allowUnfencedReviewProviderForTests.Store(true)
}

func TestRuntimeStoreWithoutBrokerFailsClosed(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedReviewProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedReviewProviderForTests.Store(true) })
	previousClient := reviewBrokerClient
	reviewBrokerClient = func() *database.Client { return nil }
	t.Cleanup(func() { reviewBrokerClient = previousClient })

	workspace := filepath.Join(t.TempDir(), "must-not-create")
	store := NewSQLiteStore(workspace)
	if store.root != "" || store.database != "" || store.StoreID() != "" ||
		database.CodeOf(store.brokerErr) != database.CodeUnavailable {
		t.Fatalf("unfenced runtime store = %#v", store)
	}
	if err := store.Preflight(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("unfenced preflight error = %v", err)
	}
	if _, err := store.LockAutomationController(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("unfenced controller lock error = %v", err)
	}
	_, _, err := store.TryLockIssueGenerationAttempt("owner/repo", "draft", "generation")
	if database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("unfenced issue lock error = %v", err)
	}
	local := newSQLiteStoreLocal(workspace)
	if database.CodeOf(local.brokerErr) != database.CodeUnauthorized || local.root != "" {
		t.Fatalf("unfenced local store = %#v", local)
	}
	if _, err := local.openDatabase(t.Context()); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced local database opener error = %v", err)
	}
	handler := newReviewStoreHandler(workspace, ReviewStoreID)
	if _, err := handler.open(); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced broker-local opener error = %v", err)
	}
	if _, err := NewBrokerHandler(t.TempDir(), nil); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced broker handler error = %v", err)
	}
	if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
		t.Fatalf("unfenced runtime store created workspace: %v", err)
	}
}
