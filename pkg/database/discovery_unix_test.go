//go:build unix

//nolint:govet // Independent boundary assertions intentionally reuse err.
package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCanonicalHomeRejectsSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "real-home")
	if _, err := PrepareHome(realHome); err != nil {
		t.Fatalf("PrepareHome() error = %v", err)
	}
	alias := filepath.Join(root, "home-alias")
	if err := os.Symlink(realHome, alias); err != nil {
		t.Fatalf("create home symlink: %v", err)
	}
	if _, err := CanonicalHome(alias); CodeOf(err) != CodeInvalid {
		t.Fatalf("CanonicalHome(alias) error = %v, want Invalid", err)
	}
	nestedAlias := filepath.Join(alias, "nested")
	if _, err := PrepareHome(nestedAlias); CodeOf(err) != CodeInvalid {
		t.Fatalf("PrepareHome(nested alias) error = %v, want Invalid", err)
	}
	canonical, err := CanonicalHome(filepath.Join(realHome, "."))
	if err != nil || canonical != realHome {
		t.Fatalf("lexical canonical home = %q, %v, want %q", canonical, err, realHome)
	}
}

func TestServerPublishesOwnerOnlyManifestAndSocket(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	manifest, err := ReadManifest(home)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}
	if manifest != server.Manifest() || len(manifest.Token) != 64 || len(manifest.Epoch) != 32 {
		t.Fatalf("manifest = %#v, server = %#v", manifest, server.Manifest())
	}
	stateDir, err := StateDirectory(home)
	if err != nil {
		t.Fatalf("StateDirectory() error = %v", err)
	}
	assertMode(t, stateDir, os.ModeDir|0o700)
	assertMode(t, filepath.Join(stateDir, manifestFileName), 0o600)
	info, err := os.Lstat(manifest.Endpoint)
	if err != nil {
		t.Fatalf("stat broker socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want socket 0600", info.Mode())
	}

	if _, err := StartServer(
		context.Background(),
		ServerOptions{Home: filepath.Join(home, ".")},
	); CodeOf(
		err,
	) != CodeAlreadyExists {
		t.Fatalf("duplicate StartServer() error = %v, want AlreadyExists", err)
	}
	if fence, err := AcquireMigrationFence(home); CodeOf(err) != CodeConflict || fence != nil {
		t.Fatalf("migration fence while broker live = %#v, %v", fence, err)
	}
	online, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatalf("second shared online fence error = %v", err)
	}
	if err := online.Close(); err != nil {
		t.Fatalf("close online fence: %v", err)
	}
}

func TestOnlineAndMigrationFenceHandoff(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	first, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatalf("AcquireOnlineFence() error = %v", err)
	}
	second, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatalf("second AcquireOnlineFence() error = %v", err)
	}
	if migration, err := AcquireMigrationFence(home); CodeOf(err) != CodeConflict || migration != nil {
		t.Fatalf("exclusive fence with readers = %#v, %v", migration, err)
	}
	_ = first.Close()
	_ = second.Close()

	migration, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatalf("AcquireMigrationFence() after readers = %v", err)
	}
	if online, err := AcquireOnlineFence(home); CodeOf(err) != CodeConflict || online != nil {
		t.Fatalf("online fence during migration = %#v, %v", online, err)
	}
	if err := migration.Close(); err != nil {
		t.Fatalf("close migration fence: %v", err)
	}
	if online, err := AcquireOnlineFence(home); err != nil {
		t.Fatalf("online fence after migration = %v", err)
	} else {
		_ = online.Close()
	}
}

func TestManifestModeAndEndpointBoundaryFailClosed(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	stateDir, _ := StateDirectory(home)
	manifestPath := filepath.Join(stateDir, manifestFileName)
	if err := os.Chmod(manifestPath, 0o644); err != nil {
		t.Fatalf("chmod manifest: %v", err)
	}
	if _, err := ReadManifest(home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("permissive manifest error = %v, want Integrity", err)
	}
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		t.Fatalf("restore manifest mode: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatalf("server.Close() error = %v", err)
	}
	if _, err := ReadManifest(home); CodeOf(err) != CodeUnavailable {
		t.Fatalf("manifest after close error = %v, want Unavailable", err)
	}

	stateDir, err = prepareStateDirectory(home)
	if err != nil {
		t.Fatalf("prepareStateDirectory() error = %v", err)
	}
	endpoint := endpointForStateDirectory(stateDir)
	if err := os.WriteFile(endpoint, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write endpoint obstruction: %v", err)
	}
	if _, err := StartServer(context.Background(), ServerOptions{Home: home}); CodeOf(err) != CodeIntegrity {
		t.Fatalf("obstructed endpoint error = %v, want Integrity", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Mode() != want {
		t.Fatalf("mode %q = %v, want %v", path, info.Mode(), want)
	}
}

func TestFenceCloseIsIdempotent(t *testing.T) {
	fence, err := AcquireMigrationFence(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatalf("AcquireMigrationFence() error = %v", err)
	}
	if err := fence.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := fence.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
