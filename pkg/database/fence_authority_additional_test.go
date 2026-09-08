//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFenceNilClosedAndWrongHomeAuthorityFailsClosed(t *testing.T) {
	var missing *Fence
	if missing.Authorizes(t.TempDir()) {
		t.Fatal("nil fence authorized a home")
	}
	if release, err := missing.Guard(t.TempDir()); release != nil || CodeOf(err) != CodeUnavailable {
		t.Fatalf("nil Guard() release=%t, err=%v", release != nil, err)
	}
	if release, err := missing.GuardMigration(t.TempDir()); release != nil || CodeOf(err) != CodeUnavailable {
		t.Fatalf("nil GuardMigration() release=%t, err=%v", release != nil, err)
	}
	if ctx, err := missing.MigrationContext(
		nil, filepath.Join(t.TempDir(), "stage.db"),
	); ctx != nil || CodeOf(err) != CodeConflict {
		t.Fatalf("nil MigrationContext() = %v, %v", ctx, err)
	}
	if err := missing.Close(); err != nil {
		t.Fatalf("nil Close() = %v", err)
	}

	home := t.TempDir()
	other := t.TempDir()
	online, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if release, guardErr := online.Guard(other); release != nil || CodeOf(guardErr) != CodeIntegrity {
		t.Fatalf("wrong-home Guard() release=%t, err=%v", release != nil, guardErr)
	}
	if release, guardErr := online.Guard("bad\x00home"); release != nil || CodeOf(guardErr) != CodeIntegrity {
		t.Fatalf("invalid-home Guard() release=%t, err=%v", release != nil, guardErr)
	}
	if ctx, contextErr := online.MigrationContext(
		nil, filepath.Join(home, "stage.db"),
	); ctx != nil || CodeOf(contextErr) != CodeConflict {
		t.Fatalf("online MigrationContext() = %v, %v", ctx, contextErr)
	}
	if err := online.Close(); err != nil {
		t.Fatal(err)
	}
	if release, guardErr := online.Guard(home); release != nil || CodeOf(guardErr) != CodeIntegrity {
		t.Fatalf("closed Guard() release=%t, err=%v", release != nil, guardErr)
	}
	if err := online.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}

	migration, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if release, guardErr := migration.GuardMigration(other); release != nil ||
		CodeOf(guardErr) != CodeUnauthorized {
		t.Fatalf("wrong-home GuardMigration() release=%t, err=%v", release != nil, guardErr)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	if release, guardErr := migration.GuardMigration(home); release != nil ||
		CodeOf(guardErr) != CodeUnauthorized {
		t.Fatalf("closed GuardMigration() release=%t, err=%v", release != nil, guardErr)
	}
}

func TestMigrationContextPresenceExactTargetAndExpiration(t *testing.T) {
	if MigrationContextPresent(nil) || MigrationContextActive(nil) ||
		MigrationContextAuthorizes(nil, filepath.Join(t.TempDir(), "stage.db")) {
		t.Fatal("nil context carried migration authority")
	}

	wrongType := context.WithValue(context.Background(), migrationContextKey{}, "not-authority")
	if MigrationContextPresent(wrongType) || MigrationContextActive(wrongType) {
		t.Fatal("wrong context value type carried migration authority")
	}
	forged := context.WithValue(
		context.Background(), migrationContextKey{}, migrationContextAuthority{},
	)
	if !MigrationContextPresent(forged) || MigrationContextActive(forged) {
		t.Fatal("forged context presence and live authority were not distinguished")
	}

	home := t.TempDir()
	target := filepath.Join(home, "stage.db")
	other := filepath.Join(home, "other.db")
	fence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := fence.MigrationContext(nil, target)
	if err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	if !MigrationContextPresent(ctx) || !MigrationContextActive(ctx) ||
		!MigrationContextAuthorizes(ctx, filepath.Clean(target)) {
		t.Fatal("live exact-target migration context was not authoritative")
	}
	if !MigrationContextActive(ctx) || MigrationContextAuthorizes(ctx, other) ||
		MigrationContextAuthorizes(ctx, "bad\x00target") {
		t.Fatal("migration context exact-target checks were not isolated")
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if !MigrationContextPresent(ctx) || MigrationContextActive(ctx) ||
		MigrationContextAuthorizes(ctx, target) {
		t.Fatal("expired capability presence and authority were not distinguished")
	}
}

func TestMigrationContextRejectsMalformedTargets(t *testing.T) {
	home := t.TempDir()
	fence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()

	for _, target := range []string{
		"", " padded", "padded ", "bad\x00target", ":memory:",
		"file:stage.db", "FILE:stage.db",
	} {
		if ctx, targetErr := fence.MigrationContext(
			context.Background(), target,
		); ctx != nil || CodeOf(targetErr) != CodeInvalid {
			t.Errorf("MigrationContext(%q) = %v, %v", target, ctx, targetErr)
		}
	}
	relative := filepath.Join("relative", "stage.db")
	ctx, err := fence.MigrationContext(context.Background(), relative)
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	if !MigrationContextAuthorizes(ctx, absolute) {
		t.Fatal("relative target did not bind its canonical absolute identity")
	}
}

func TestFenceIdentityHelpersRejectUnavailableBoundaries(t *testing.T) {
	if info, err := fenceLockIdentity(
		nil, filepath.Join(t.TempDir(), "missing"),
	); info != nil || CodeOf(err) != CodeIntegrity {
		t.Fatalf("fenceLockIdentity(nil) = %#v, %v", info, err)
	}
	if homeInfo, stateInfo, err := fenceBoundaryIdentity(
		filepath.Join(t.TempDir(), "missing"),
	); homeInfo != nil || stateInfo != nil || CodeOf(err) != CodeIntegrity {
		t.Fatalf("fenceBoundaryIdentity(missing) = %#v, %#v, %v", homeInfo, stateInfo, err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if info, identityErr := fenceLockIdentity(file, path); identityErr != nil || info == nil {
		_ = file.Close()
		t.Fatalf("fenceLockIdentity(valid) = %#v, %v", info, identityErr)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if info, identityErr := fenceLockIdentity(
		file, path,
	); info != nil || CodeOf(identityErr) != CodeIntegrity {
		t.Fatalf("fenceLockIdentity(closed) = %#v, %v", info, identityErr)
	}
}
