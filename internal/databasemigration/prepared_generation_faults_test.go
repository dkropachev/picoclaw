package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestPreparedGenerationSealRejectsUnsafeExpectedMembers(t *testing.T) {
	databasePayload := []byte("database")
	walPayload := []byte("wal")
	manifest := generationManifestForPayloads(t, databasePayload, walPayload)
	spec := storecatalog.Spec{ID: "global/auth", Path: manifest.Stores[0].Path}

	for _, test := range []struct {
		name string
		make func(*testing.T, string)
		want string
	}{
		{
			name: "directory",
			make: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "unsafe",
		},
		{
			name: "symlink",
			make: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "target")
				writeMigrationFile(t, target, walPayload)
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			want: "unsafe",
		},
		{
			name: "unreadable",
			make: func(t *testing.T, path string) {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("Unix unreadable-file assertion")
				}
				writeMigrationFile(t, path, walPayload)
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
			},
			want: "permission",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := migrationHome(t)
			path := filepath.Join(root, "generation.db")
			writeMigrationFile(t, path, databasePayload)
			test.make(t, path+"-wal")
			prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
			if prepared != nil || err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("unsafe %s seal = %#v, %v", test.name, prepared, err)
			}
		})
	}
}

func TestPreparedGenerationSealClosesEarlierMembersOnCancellation(t *testing.T) {
	databasePayload := []byte("database")
	walPayload := []byte("wal")
	manifest := generationManifestForPayloads(t, databasePayload, walPayload)
	spec := storecatalog.Spec{ID: "global/auth", Path: manifest.Stores[0].Path}
	root := migrationHome(t)
	path := filepath.Join(root, "generation.db")
	writeMigrationFile(t, path, databasePayload)
	writeMigrationFile(t, path+"-wal", walPayload)
	ctx := &cancelAfterMigrationErrChecks{Context: context.Background(), allowed: 3}
	prepared, err := sealPreparedGeneration(ctx, path, manifest, spec)
	if prepared != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-seal cancellation = %#v, %v", prepared, err)
	}
}

func TestPreparedGenerationSealRejectsPublicAndWrongSizeMembers(t *testing.T) {
	payload := []byte("database")
	manifest := preparedGenerationFixture(t, payload)
	spec := storecatalog.Spec{ID: "global/auth", Path: manifest.Stores[0].Path}

	t.Run("wrong size", func(t *testing.T) {
		root := migrationHome(t)
		path := filepath.Join(root, "generation.db")
		writeMigrationFile(t, path, append(payload, '!'))
		prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
		if prepared != nil || err == nil || !strings.Contains(err.Error(), "size") {
			t.Fatalf("wrong-size seal = %#v, %v", prepared, err)
		}
	})

	t.Run("public mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix public-mode assertion")
		}
		root := migrationHome(t)
		path := filepath.Join(root, "generation.db")
		writeMigrationFile(t, path, payload)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
		if prepared != nil || err == nil || !strings.Contains(err.Error(), "validate") {
			t.Fatalf("public-member seal = %#v, %v", prepared, err)
		}
	})
}

func TestPreparedGenerationGuardRejectsClosedAndRemovedObjects(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *preparedGeneration)
		want   string
	}{
		{
			name: "closed root handle",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				if err := prepared.rootHandle.Close(); err != nil {
					t.Fatal(err)
				}
			},
			want: "root handle",
		},
		{
			name: "closed root descriptor",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				if err := prepared.rootFile.Close(); err != nil {
					t.Fatal(err)
				}
			},
			want: "root identity",
		},
		{
			name: "closed member descriptor",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				if err := prepared.members[0].file.Close(); err != nil {
					t.Fatal(err)
				}
			},
			want: "seal changed",
		},
		{
			name: "removed member name",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				if err := os.Remove(prepared.members[0].path); err != nil {
					t.Fatal(err)
				}
			},
			want: "inventory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, spec, session := additionalGenerationSnapshot(t)
			prepared, cleanup, err := session.prepareGeneration(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cleanup() })
			test.mutate(t, prepared)
			if err := prepared.guard(t.Context()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("guard after %s = %v", test.name, err)
			}
		})
	}
}

func TestPreparedGenerationEmptyInventoryIsBounded(t *testing.T) {
	manifest := validGenerationManifestFixture(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: manifest.Stores[0].Path}
	root := migrationHome(t)
	path := filepath.Join(root, "generation.db")
	prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.close()
	writeMigrationFile(t, filepath.Join(root, "unexpected"), []byte("extra"))
	if err := prepared.guard(t.Context()); err == nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("empty sealed inventory growth = %v", err)
	}
}

func generationManifestForPayloads(
	t *testing.T,
	databasePayload,
	walPayload []byte,
) BackupManifest {
	t.Helper()
	manifest := validGenerationManifestFixture(t, "database", "wal")
	for index, payload := range [][]byte{databasePayload, walPayload} {
		digest := sha256.Sum256(payload)
		manifest.Files[index].SHA256 = hex.EncodeToString(digest[:])
		manifest.Files[index].Size = int64(len(payload))
	}
	return manifest
}
