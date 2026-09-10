package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestPreparedGenerationSealSkipsOtherStoreRecords(t *testing.T) {
	home := migrationHome(t)
	primary := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	other := storecatalog.Spec{ID: "global/history", Path: filepath.Join(home, "history.db")}
	writeMigrationFile(t, primary.Path, []byte("auth database"))
	writeMigrationFile(t, other.Path, []byte("history database"))
	session := additionalSnapshot(
		t, home,
		[]storecatalog.Spec{primary, other},
		[]storecatalog.Spec{primary, other},
	)

	prepared, cleanup, err := session.prepareGeneration(t.Context(), primary)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			t.Errorf("clean prepared generation: %v", cleanupErr)
		}
	}()
	if prepared.storeID != primary.ID.String() || len(prepared.members) != 1 ||
		prepared.members[0].role != "database" {
		t.Fatalf("prepared wrong store records: %#v", prepared)
	}
}

func TestPreparedGenerationSealCleansUpAfterFinalGuardCancellation(t *testing.T) {
	payload := []byte("database")
	manifest := preparedGenerationFixture(t, payload)
	spec := storecatalog.Spec{ID: "global/auth", Path: manifest.Stores[0].Path}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "generation.db")
	writeMigrationFile(t, path, payload)

	// One member consumes three cancellation checks while it is selected and
	// hashed, and the three absent sidecars consume one each. Cancellation on
	// the next check therefore exercises the final, fully opened guard.
	ctx := &cancelAfterMigrationErrChecks{Context: context.Background(), allowed: 6}
	prepared, err := sealPreparedGeneration(ctx, path, manifest, spec)
	if prepared != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("final-guard cancellation = %#v, %v", prepared, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove member after failed seal cleanup: %v", err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatalf("remove root after failed seal cleanup: %v", err)
	}
}

func TestPreparedGenerationSealReportsRootOpenFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory-mode fault injection")
	}
	manifest := validGenerationManifestFixture(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: manifest.Stores[0].Path}

	for _, test := range []struct {
		name  string
		mode  os.FileMode
		probe func(*os.Root) error
	}{
		{
			name: "root handle",
			mode: 0,
			probe: func(root *os.Root) error {
				if root != nil {
					return root.Close()
				}
				return os.ErrPermission
			},
		},
		{
			name: "root descriptor",
			mode: 0o400,
			probe: func(root *os.Root) error {
				if root == nil {
					return nil
				}
				file, err := root.Open(".")
				if file != nil {
					_ = file.Close()
				}
				return errors.Join(err, root.Close())
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, test.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

			probeRoot, probeErr := os.OpenRoot(root)
			if test.name == "root handle" {
				if probeErr == nil {
					_ = probeRoot.Close()
					t.Skip("directory permissions are not enforced for os.OpenRoot")
				}
			} else {
				if probeErr != nil {
					t.Skipf("os.OpenRoot cannot isolate descriptor failure: %v", probeErr)
				}
				if err := test.probe(probeRoot); err == nil {
					t.Skip("directory permissions do not isolate the root descriptor open")
				}
			}

			path := filepath.Join(root, "generation.db")
			prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
			if prepared != nil || err == nil || !errors.Is(err, os.ErrPermission) {
				t.Fatalf("%s failure = %#v, %v", test.name, prepared, err)
			}
		})
	}
}

func TestPreparedGenerationGuardRejectsMissingExpectedInventory(t *testing.T) {
	prepared, cleanup := newAdditionalPreparedGeneration(t)
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			t.Errorf("clean prepared generation: %v", cleanupErr)
		}
	}()
	originalMembers := prepared.members
	prepared.members = append(prepared.members, preparedGenerationMember{
		role: "wal", path: prepared.path + "-wal",
	})
	err := prepared.guard(t.Context())
	prepared.members = originalMembers
	if err == nil || !strings.Contains(err.Error(), "inventory changed") {
		t.Fatalf("missing expected inventory guard = %v", err)
	}
}

func TestPreparedGenerationGuardRevalidatesPrivateMemberFromPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission assertion")
	}
	prepared, cleanup := newAdditionalPreparedGeneration(t)
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			t.Errorf("clean prepared generation: %v", cleanupErr)
		}
	}()
	member := &prepared.members[0]
	if err := os.Chmod(member.path, 0o644); err != nil {
		t.Fatal(err)
	}
	current, err := member.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	member.info = current
	guardErr := prepared.guard(t.Context())
	if err := os.Chmod(member.path, 0o600); err != nil {
		t.Fatal(err)
	}
	if guardErr == nil || !strings.Contains(guardErr.Error(), "validate prepared generation member") {
		t.Fatalf("public member with refreshed cache guard = %v", guardErr)
	}
}

func TestPreparedGenerationGuardRejectsDescriptorAndIdentityMismatch(t *testing.T) {
	t.Run("root descriptor", func(t *testing.T) {
		prepared, cleanup := newAdditionalPreparedGeneration(t)
		defer func() { _ = cleanup() }()
		rootFile := prepared.rootFile
		prepared.rootFile = prepared.members[0].file
		err := prepared.guard(t.Context())
		prepared.rootFile = rootFile
		if err == nil || !strings.Contains(err.Error(), "root identity changed") {
			t.Fatalf("mismatched root descriptor guard = %v", err)
		}
	})

	t.Run("member descriptor", func(t *testing.T) {
		prepared, cleanup := newAdditionalPreparedGeneration(t)
		defer func() { _ = cleanup() }()
		memberFile := prepared.members[0].file
		prepared.members[0].file = prepared.rootFile
		err := prepared.guard(t.Context())
		prepared.members[0].file = memberFile
		if err == nil || !strings.Contains(err.Error(), "member seal changed") {
			t.Fatalf("mismatched member descriptor guard = %v", err)
		}
	})

	t.Run("member identity", func(t *testing.T) {
		prepared, cleanup := newAdditionalPreparedGeneration(t)
		defer func() { _ = cleanup() }()
		identity := prepared.members[0].identity
		prepared.members[0].identity = prepared.rootIdentity
		err := prepared.guard(t.Context())
		prepared.members[0].identity = identity
		if err == nil || !strings.Contains(err.Error(), "member seal changed") {
			t.Fatalf("mismatched member identity guard = %v", err)
		}
	})
}

func TestPreparedGenerationCloseAggregatesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first")
	secondPath := filepath.Join(root, "second")
	writeMigrationFile(t, firstPath, []byte("first"))
	writeMigrationFile(t, secondPath, []byte("second"))
	first, err := os.Open(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Open(secondPath)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		_ = first.Close()
		_ = second.Close()
		t.Fatal(err)
	}
	rootFile, err := rootHandle.Open(".")
	if err != nil {
		_ = first.Close()
		_ = second.Close()
		_ = rootHandle.Close()
		t.Fatal(err)
	}
	prepared := &preparedGeneration{
		rootHandle: rootHandle,
		rootFile:   rootFile,
		members: []preparedGenerationMember{
			{file: first},
			{file: second},
		},
	}
	t.Cleanup(func() { _ = prepared.close() })
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	closeErr := prepared.close()
	if !errors.Is(closeErr, os.ErrClosed) {
		t.Fatalf("aggregated close error = %v", closeErr)
	}
	if _, err := second.Stat(); err == nil {
		t.Fatal("close stopped after the first member error")
	}
	if _, err := rootFile.Stat(); err == nil {
		t.Fatal("close left the root descriptor open")
	}
	if _, err := rootHandle.Stat("."); err == nil {
		t.Fatal("close left the root handle open")
	}
	if err := prepared.close(); err != nil {
		t.Fatalf("idempotent close = %v", err)
	}
}

func newAdditionalPreparedGeneration(t *testing.T) (*preparedGeneration, func() error) {
	t.Helper()
	_, spec, session := additionalGenerationSnapshot(t)
	prepared, cleanup, err := session.prepareGeneration(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return prepared, cleanup
}
