//go:build unix

//nolint:govet,golines,misspell // Dense boundary fixtures intentionally reuse narrow scopes.
package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectRejectsInvalidInputsAndUnsafeAncestors(t *testing.T) {
	if inspection, err := Inspect(nil, "store.db", time.Second); inspection.Exists || err == nil {
		t.Fatalf("nil-context Inspect() = %#v, %v", inspection, err)
	}
	for _, path := range []string{"", ":memory:"} {
		if inspection, err := Inspect(context.Background(), path, time.Second); inspection.Exists || err == nil {
			t.Errorf("Inspect(%q) = %#v, %v", path, inspection, err)
		}
	}

	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if inspection, err := Inspect(t.Context(), filepath.Join(alias, "store.db"), time.Second); inspection.Exists || err == nil {
		t.Fatalf("symlink-ancestor Inspect() = %#v, %v", inspection, err)
	}
}

func TestInspectPropagatesEndpointInspectionFailure(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "blocked")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	inspection, err := Inspect(t.Context(), filepath.Join(parent, "store.db"), time.Second)
	if inspection.Exists || err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uninspectable endpoint Inspect() = %#v, %v", inspection, err)
	}
}

func TestInspectRejectsHardlinkedGenerationAndPoolConfigurationDrift(t *testing.T) {
	t.Run("hardlinked generation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, path+".alias"); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		if inspection, err := Inspect(t.Context(), path, time.Second); inspection.Exists ||
			!IsInspectionIntegrity(err) {
			t.Fatalf("hardlinked Inspect() = %#v, %v", inspection, err)
		}
	})

	t.Run("pool timeout", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		database, err := OpenStore(path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := Configure(t.Context(), database, time.Second, false); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		first, err := Inspect(t.Context(), path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Release()
		if second, err := Inspect(t.Context(), path, 2*time.Second); second.Exists ||
			!IsInspectionIntegrity(err) {
			t.Fatalf("changed-timeout Inspect() = %#v, %v", second, err)
		}
	})

	t.Run("reused closed pool", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		database, err := OpenStore(path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := Configure(t.Context(), database, time.Second, false); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		first, err := Inspect(t.Context(), path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := first.database.Close(); err != nil {
			t.Fatal(err)
		}
		if second, err := Inspect(t.Context(), path, time.Second); second.Exists || err == nil {
			t.Fatalf("closed-pool Inspect() = %#v, %v", second, err)
		}
		if err := first.Release(); err != nil {
			t.Fatal(err)
		}
	})
}
