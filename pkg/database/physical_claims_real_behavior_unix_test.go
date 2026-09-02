//go:build unix

//nolint:govet // Independent physical-claim assertions intentionally use narrow errors.
package database

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestPhysicalClaimsPropagateRealLockFileFailure(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}
	catalog, err := storecatalog.Project(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Specs) == 0 {
		t.Fatal("projected catalog is empty")
	}
	claimRoot, err := preparePhysicalClaimRoot()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(filepath.Clean(catalog.Specs[0].Path)))
	blocker := filepath.Join(claimRoot, hex.EncodeToString(digest[:])+".lock")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(blocker) })
	claims, err := AcquireCatalogStoreClaims(home, cfg)
	if claims != nil || err == nil || CodeOf(err) == CodeConflict {
		t.Fatalf("blocked physical claim = %#v, %v", claims, err)
	}
}

func TestPhysicalClaimsRejectGenerationThatChangesProjectionSemantics(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "auth.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	claims, err := AcquireCatalogStoreClaims(home, &config.Config{})
	if claims != nil || err == nil {
		t.Fatalf("unsafe physical generation = %#v, %v", claims, err)
	}
}

func TestPhysicalClaimRootRejectsRealCacheAliasesAndPermissions(t *testing.T) {
	t.Run("symlink alias", func(t *testing.T) {
		base := t.TempDir()
		realCache := filepath.Join(base, "real-cache")
		if err := os.Mkdir(realCache, 0o700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(base, "cache-alias")
		if err := os.Symlink(realCache, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Setenv("XDG_CACHE_HOME", alias)
		if root, err := preparePhysicalClaimRoot(); err == nil || root != "" {
			t.Fatalf("symlinked cache root = %q, %v", root, err)
		}
	})

	t.Run("non-writable cache", func(t *testing.T) {
		cache := t.TempDir()
		if err := os.Chmod(cache, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cache, 0o700) })
		t.Setenv("XDG_CACHE_HOME", cache)
		root, err := preparePhysicalClaimRoot()
		if err == nil {
			_ = os.RemoveAll(root)
			t.Skip("current user can create files in a non-writable cache directory")
		}
		if root != "" {
			t.Fatalf("failed cache root = %q, %v", root, err)
		}
	})
}
