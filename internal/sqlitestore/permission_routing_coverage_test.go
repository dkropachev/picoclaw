package sqlitestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
)

func TestOpenRoutesGenerationThroughProviderValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	canary := errors.New("provider generation validation")
	called := false
	swapTestHook(t, &secureOpenedSQLiteFiles, func(candidate string) error {
		called = true
		if candidate != path {
			t.Fatalf("provider generation path = %q, want %q", candidate, path)
		}
		return canary
	})
	database, err := Open(t.Context(), path, testOptions())
	if database != nil {
		_ = database.Close()
		t.Fatal("Open() returned a database after provider validation failed")
	}
	if !errors.Is(err, canary) {
		t.Fatalf("Open() error = %v, want provider validation failure", err)
	}
	if !called {
		t.Fatal("Open() did not route generation validation through the provider")
	}
}

func TestProviderGenerationValidationAllowsOnlyVanishedCompanions(t *testing.T) {
	t.Run("WAL companion", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		wal := path + "-wal"
		for _, candidate := range []string{path, wal} {
			if err := os.WriteFile(candidate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(wal); err != nil {
			t.Fatal(err)
		}
		if err := sqliteprovider.SecureGeneration(path); err != nil {
			t.Fatalf("vanished WAL companion: %v", err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("primary database disappeared: %v", err)
		}
		if _, err := os.Lstat(wal); !os.IsNotExist(err) {
			t.Fatalf("WAL companion still exists: %v", err)
		}
	})

	t.Run("primary database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := sqliteprovider.SecureGeneration(path); err == nil {
			t.Fatal("provider accepted a vanished primary database")
		}
	})
}

func TestLegacyArchiveDirectoryCreationRoutesThroughPrivateValidation(t *testing.T) {
	tests := []struct {
		name string
		call func(*testing.T, string) error
	}{
		{
			name: "archive root",
			call: func(t *testing.T, root string) error {
				return ensureLegacyArchiveRoot(root, filepath.Join(root, "legacy-json", "component-v1"))
			},
		},
		{
			name: "archive parent",
			call: func(t *testing.T, root string) error {
				return ensureArchiveParent(root, filepath.Join(root, "nested", "archive", "source.json"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			original := legacyArchiveChmod
			replaced := false
			swapTestHook(t, &legacyArchiveChmod, func(path string, mode os.FileMode) error {
				if err := original(path, mode); err != nil {
					return err
				}
				if replaced {
					return nil
				}
				if err := os.Remove(path); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
					return err
				}
				replaced = true
				return nil
			})
			callErr := test.call(t, root)
			if callErr == nil {
				t.Fatal("archive directory replacement after chmod was accepted")
			}
			if !replaced {
				t.Fatalf("test did not replace an archive directory after chmod: %v", callErr)
			}
		})
	}
}
