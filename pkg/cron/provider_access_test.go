package cron

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	// Legacy package tests exercise provider internals directly. Production
	// callers must use the runtime broker or hold an owner fence.
	allowUnfencedCronProviderForTests.Store(true)
}

func TestRuntimeConstructorWithoutBrokerFailsClosed(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedCronProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedCronProviderForTests.Store(true) })
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previousClient) })

	workspace := filepath.Join(t.TempDir(), "must-not-create")
	service := NewForWorkspace(filepath.Join(workspace, cronDatabaseFilename), nil)
	if service == nil || database.CodeOf(service.initErr) != database.CodeUnavailable {
		t.Fatalf("unfenced runtime service = %#v, error %v", service, service.initErr)
	}
	if service.storage != nil || service.storePath != filepath.Join(workspace, cronDatabaseFilename) {
		t.Fatalf("unfenced runtime service acquired local storage: %#v", service)
	}
	_, err := newCronSQLiteStorage(filepath.Join(workspace, cronDatabaseFilename))
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced provider constructor error = %v", err)
	}
	_, err = newLocalCronService(filepath.Join(workspace, cronDatabaseFilename), nil)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced local service error = %v", err)
	}
	if _, err := NewBrokerHandler(t.TempDir(), nil); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced broker handler error = %v", err)
	}
	if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
		t.Fatalf("unfenced runtime constructor created workspace: %v", err)
	}
}
