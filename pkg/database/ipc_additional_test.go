package database

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCatalogFingerprintValidation(t *testing.T) {
	value := catalogFingerprintPrefix + strings.Repeat("a", 63) + "g"
	if validCatalogFingerprint(value) {
		t.Fatalf("non-hex catalog fingerprint %q was accepted", value)
	}
	if !validCatalogFingerprint(catalogFingerprintPrefix + strings.Repeat("a", 64)) {
		t.Fatal("valid catalog fingerprint was rejected")
	}
}

func TestMigrationFenceRejectsInvalidHome(t *testing.T) {
	fence, err := AcquireMigrationFence("invalid\x00home")
	if fence != nil || CodeOf(err) != CodeInvalid {
		t.Fatalf("migration fence for invalid home = %#v, %v", fence, err)
	}
}

func TestFenceOwnershipTracksExactLiveHome(t *testing.T) {
	home := t.TempDir()
	online, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if !online.Authorizes(home) || online.Authorizes(t.TempDir()) {
		t.Fatal("online fence exact-home authority is incorrect")
	}
	if closeErr := online.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	migration, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if !migration.Authorizes(home) {
		t.Fatal("migration fence exact-home authority is incorrect")
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	if migration.Authorizes(home) {
		t.Fatal("closed migration fence retained authority")
	}
}

func TestMigrationContextRequiresLiveExactTargetFence(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "stage.db")
	fence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := fence.MigrationContext(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !MigrationContextActive(ctx) || !MigrationContextAuthorizes(ctx, target) {
		t.Fatal("live migration context did not authorize exact target")
	}
	if MigrationContextAuthorizes(ctx, filepath.Join(home, "other.db")) {
		t.Fatal("migration context authorized another target")
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if MigrationContextActive(ctx) || MigrationContextAuthorizes(ctx, target) {
		t.Fatal("closed migration fence left context active")
	}
	if renewed, err := fence.MigrationContext(t.Context(), target); err == nil || renewed != nil {
		t.Fatalf("closed fence context = %#v, %v", renewed, err)
	}
}

func TestFenceAuthorizesConcurrentClose(t *testing.T) {
	home := t.TempDir()
	fence, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for range 100 {
				_ = fence.Authorizes(home)
			}
		}()
	}
	close(start)
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if fence.Authorizes(home) {
		t.Fatal("closed fence retained authority")
	}
}

func TestFenceGuardDelaysCloseAndRequiresMigrationCapability(t *testing.T) {
	home := t.TempDir()
	online, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if release, guardErr := online.GuardMigration(home); release != nil ||
		CodeOf(guardErr) != CodeUnauthorized {
		t.Fatalf("online GuardMigration release=%t, err=%v", release != nil, guardErr)
	}
	release, err := online.Guard(home)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		close(started)
		closed <- online.Close()
	}()
	<-started
	select {
	case closeErr := <-closed:
		t.Fatalf("fence closed through live guard: %v", closeErr)
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case closeErr := <-closed:
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("fence close did not resume after guard release")
	}

	migration, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	release, err = migration.GuardMigration(home)
	if err != nil {
		_ = migration.Close()
		t.Fatal(err)
	}
	release()
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRediscoveringClientPreservesFailureWhenManifestDisappears(t *testing.T) {
	server, err := StartServer(context.Background(), ServerOptions{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	client, err := Connect(server.home)
	if err != nil {
		_ = server.Close(context.Background())
		t.Fatal(err)
	}
	if client.Epoch() != server.Manifest().Epoch {
		t.Fatal("connected client did not retain the manifest epoch")
	}
	if closeErr := server.Close(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	var output EmptyPayload
	err = client.Call(t.Context(), "test-domain", 1, "read", EmptyPayload{}, &output)
	if databaseErr := (*Error)(nil); !errors.As(err, &databaseErr) || CodeOf(err) != CodeUnavailable {
		t.Fatalf("stale rediscovering client error = %v", err)
	}
}

func TestNilClientShutdownReturnsUnavailable(t *testing.T) {
	var client *Client
	if err := client.Shutdown(t.Context()); CodeOf(err) != CodeUnavailable {
		t.Fatalf("nil client shutdown error = %v", err)
	}
}
