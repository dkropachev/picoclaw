package dashboardauth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

func TestStoreConstructorsAndPasswordStateEdges(t *testing.T) {
	dir := t.TempDir()
	store, openErr := New(dir)
	if openErr != nil {
		t.Fatalf("New() error = %v", openErr)
	}
	if store.StoreID() != "launcher.auth" {
		t.Fatalf("StoreID() = %q", store.StoreID())
	}
	initialized, stateErr := store.IsInitialized(t.Context())
	if stateErr != nil || initialized {
		t.Fatalf("initial IsInitialized() = %t, %v", initialized, stateErr)
	}
	verified, verifyErr := store.VerifyPassword(t.Context(), "missing")
	if verifyErr != nil || verified {
		t.Fatalf("empty VerifyPassword() = %t, %v", verified, verifyErr)
	}
	if err := store.SetPassword(t.Context(), ""); err == nil {
		t.Fatal("SetPassword() accepted an empty password")
	}
	if err := store.SetPassword(t.Context(), strings.Repeat("x", 73)); err == nil {
		t.Fatal("SetPassword() accepted a bcrypt-oversized password")
	}
	if err := store.SetPassword(t.Context(), "correct password"); err != nil {
		t.Fatal(err)
	}
	initialized, stateErr = store.IsInitialized(t.Context())
	if stateErr != nil || !initialized {
		t.Fatalf("saved IsInitialized() = %t, %v", initialized, stateErr)
	}
	verified, verifyErr = store.VerifyPassword(t.Context(), "wrong password")
	if verifyErr != nil || verified {
		t.Fatalf("wrong VerifyPassword() = %t, %v", verified, verifyErr)
	}
	verified, verifyErr = store.VerifyPassword(t.Context(), "correct password")
	if verifyErr != nil || !verified {
		t.Fatalf("correct VerifyPassword() = %t, %v", verified, verifyErr)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.IsInitialized(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled IsInitialized() error = %v", err)
	}
	if _, err := store.VerifyPassword(canceled, "correct password"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled VerifyPassword() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IsInitialized(t.Context()); err == nil {
		t.Fatal("closed IsInitialized() unexpectedly succeeded")
	}
	if err := store.SetPassword(t.Context(), "after close"); err == nil {
		t.Fatal("closed SetPassword() unexpectedly succeeded")
	}
	if _, err := store.VerifyPassword(t.Context(), "after close"); err == nil {
		t.Fatal("closed VerifyPassword() unexpectedly succeeded")
	}
}

func TestStoreConstructorRejectsInvalidLauncherConfigPath(t *testing.T) {
	dir := t.TempDir()
	badConfig := filepath.Join(dir, "not-"+launcherconfig.FileName)
	if store, err := NewWithLauncherConfig(dir, badConfig); err == nil {
		store.Close()
		t.Fatal("NewWithLauncherConfig() accepted a noncanonical config name")
	}
	if store, err := OpenWithLauncherConfig(filepath.Join(dir, databaseFilename), badConfig); err == nil {
		store.Close()
		t.Fatal("OpenWithLauncherConfig() accepted a noncanonical config name")
	}
}

func TestVerifyPasswordRejectsMalformedStoredHash(t *testing.T) {
	store, openErr := Open(filepath.Join(t.TempDir(), databaseFilename))
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer store.Close()
	if _, err := store.db.Exec(sqlUpsertHash, "not-a-bcrypt-hash"); err != nil {
		t.Fatal(err)
	}
	if verified, err := store.VerifyPassword(t.Context(), "password"); err == nil || verified {
		t.Fatalf("VerifyPassword(malformed) = %t, %v", verified, err)
	}
}
