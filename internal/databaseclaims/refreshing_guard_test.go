package databaseclaims

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestGuardStoresRefreshingClaimsMaterializedMembersAndRejectsReplacement(t *testing.T) {
	t.Run("materialized generation", func(t *testing.T) {
		lease, _ := acquireRefreshingGuardLease(t, false)
		stores, reconcile, release, err := lease.GuardStoresRefreshing()
		if err != nil || len(stores) == 0 || reconcile == nil || release == nil {
			t.Fatalf("GuardStoresRefreshing = %d, %p, %p, %v", len(stores), reconcile, release, err)
		}
		auth, ok := lease.byID["global/auth"]
		if !ok {
			release()
			t.Fatal("auth store is absent")
		}
		path := lease.stores[auth].Path
		before := len(lease.identities)
		if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
			release()
			t.Fatal(err)
		}
		if err := os.WriteFile(path+"-wal", []byte("wal"), 0o600); err != nil {
			release()
			t.Fatal(err)
		}
		if err := reconcile(); err != nil {
			release()
			t.Fatal(err)
		}
		if got := len(lease.identities); got < before+2 {
			release()
			t.Fatalf("materialized identities = %d, want at least %d", got, before+2)
		}
		release()
		if err := reconcile(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("reconcile after release = %v", err)
		}
	})

	t.Run("main replacement", func(t *testing.T) {
		lease, path := acquireRefreshingGuardLease(t, true)
		_, reconcile, release, err := lease.GuardStoresRefreshing()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, path+".old"); err != nil {
			release()
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			release()
			t.Fatal(err)
		}
		if err := reconcile(); database.CodeOf(err) != database.CodeIntegrity {
			release()
			t.Fatalf("replacement reconcile = %v", err)
		}
		if err := reconcile(); database.CodeOf(err) != database.CodeIntegrity {
			release()
			t.Fatalf("repeat poisoned reconcile = %v", err)
		}
		release()
	})
}

func TestGuardStoresRefreshingClaimsSQLiteInspectionSidecarsExactly(t *testing.T) {
	const providerTimeout = 5 * time.Second
	lease, path := acquireRefreshingGuardLease(t, false)
	databaseHandle, err := sqliteprovider.OpenStore(path, providerTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if configureErr := sqliteprovider.Configure(
		t.Context(), databaseHandle, providerTimeout, false,
	); configureErr != nil {
		_ = databaseHandle.Close()
		t.Fatal(configureErr)
	}
	if _, execErr := databaseHandle.Exec(`CREATE TABLE item(id INTEGER PRIMARY KEY) STRICT`); execErr != nil {
		_ = databaseHandle.Close()
		t.Fatal(execErr)
	}
	if closeErr := databaseHandle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	_, reconcile, release, err := lease.GuardStoresRefreshing()
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := sqliteprovider.InspectClaimed(
		t.Context(), path, providerTimeout, reconcile,
	)
	if err != nil {
		release()
		t.Fatal(err)
	}
	for _, member := range []string{path, path + "-wal", path + "-shm"} {
		identity, exists, identityErr := fileidentity.Existing(member)
		if identityErr != nil || !exists {
			_ = inspection.Release()
			release()
			t.Fatalf("inspection member %s = %#v, %t, %v", filepath.Base(member), identity, exists, identityErr)
		}
		claimIdentity := digestPhysicalIdentity(identity)
		if _, held := lease.identities[claimIdentity]; !held {
			_ = inspection.Release()
			release()
			t.Fatalf("inspection member %s physical claim is not held", filepath.Base(member))
		}
	}
	if err := inspection.Release(); err != nil {
		release()
		t.Fatal(err)
	}
	if err := reconcile(); err != nil {
		release()
		t.Fatal(err)
	}
	release()
}

func TestGuardStoresRefreshingSerializesReleaseWithLeaseClose(t *testing.T) {
	lease, _ := acquireRefreshingGuardLease(t, false)
	_, reconcile, release, err := lease.GuardStoresRefreshing()
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- lease.Close() }()
	select {
	case err := <-closed:
		release()
		t.Fatalf("Lease.Close passed live refreshing guard: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if err := reconcile(); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Lease.Close did not resume after refreshing guard release")
	}
}

func acquireRefreshingGuardLease(t *testing.T, withMain bool) (*Lease, string) {
	t.Helper()
	home := secureTestDir(t)
	path := filepath.Join(home, "auth.db")
	if withMain {
		if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	lease, err := newTestClaimAcquirer(t).Acquire(
		testOptions(t, home, &config.Config{}),
		fence,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return lease, path
}
