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

func TestPreparedGenerationSealRequiresExactManifestBytesAndRoles(t *testing.T) {
	payload := []byte("expected generation")
	manifest := preparedGenerationFixture(t, payload)
	spec := storecatalog.Spec{ID: "global/auth", Path: manifest.Stores[0].Path}

	t.Run("same-size changed bytes", func(t *testing.T) {
		root := migrationHome(t)
		path := filepath.Join(root, "generation.db")
		changed := append([]byte(nil), payload...)
		changed[0] ^= 0x20
		writeMigrationFile(t, path, changed)
		prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
		if prepared != nil || err == nil || !strings.Contains(err.Error(), "manifest") {
			t.Fatalf("changed-byte seal = %#v, %v", prepared, err)
		}
	})

	t.Run("missing role", func(t *testing.T) {
		root := migrationHome(t)
		prepared, err := sealPreparedGeneration(
			t.Context(), filepath.Join(root, "generation.db"), manifest, spec,
		)
		if prepared != nil || err == nil || !strings.Contains(err.Error(), "omits") {
			t.Fatalf("missing-role seal = %#v, %v", prepared, err)
		}
	})

	t.Run("unmanifested recognized sidecar", func(t *testing.T) {
		root := migrationHome(t)
		path := filepath.Join(root, "generation.db")
		writeMigrationFile(t, path, payload)
		writeMigrationFile(t, path+"-wal", []byte("unmanifested wal"))
		prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
		if prepared != nil || err == nil || !strings.Contains(err.Error(), "unmanifested") {
			t.Fatalf("unmanifested-role seal = %#v, %v", prepared, err)
		}
	})

	t.Run("hard-linked member", func(t *testing.T) {
		root := migrationHome(t)
		path := filepath.Join(root, "generation.db")
		writeMigrationFile(t, path, payload)
		if err := os.Link(path, filepath.Join(t.TempDir(), "alias")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
		if prepared != nil || err == nil || !strings.Contains(err.Error(), "link") {
			t.Fatalf("hard-linked seal = %#v, %v", prepared, err)
		}
	})
}

//nolint:dupl // Generation and legacy seals intentionally share the same drift contract.
func TestPreparedGenerationGuardRejectsMetadataInventoryAndByteDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *preparedGeneration)
		want   string
	}{
		{
			name: "member bytes",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				mutatePreparedFileBytes(t, prepared.members[0].path, prepared.members[0].info)
			},
			want: "bytes",
		},
		{
			name: "extra inventory",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				writeMigrationFile(t, filepath.Join(prepared.root, "unexpected"), []byte("extra"))
			},
			want: "inventory",
		},
		{
			name: "outside hard link",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				if err := os.Link(prepared.members[0].path, filepath.Join(t.TempDir(), "alias")); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			},
			want: "link",
		},
		{
			name: "public member",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("Unix permission assertion")
				}
				if err := os.Chmod(prepared.members[0].path, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "seal changed",
		},
		{
			name: "public root",
			mutate: func(t *testing.T, prepared *preparedGeneration) {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("Unix permission assertion")
				}
				if err := os.Chmod(prepared.root, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "validate",
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

func TestPreparedGenerationUseRechecksAfterCallback(t *testing.T) {
	_, spec, session := additionalGenerationSnapshot(t)
	prepared, cleanup, err := session.prepareGeneration(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := prepared.use(t.Context(), func(_ context.Context, path string) error {
		mutatePreparedFileBytes(t, path, prepared.members[0].info)
		return nil
	}); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("mutating prepared callback = %v", err)
	}
}

//nolint:dupl // Generation and legacy seals intentionally share the same drift contract.
func TestPreparedLegacyInputsGuardedUseAndDrift(t *testing.T) {
	t.Run("ordered synchronous use", func(t *testing.T) {
		_, spec, session := additionalLiveSnapshot(t)
		prepared, cleanup, err := session.prepareLegacyInputs(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if err := prepared.use(nil, func(_ context.Context, roots []string) error {
			if len(roots) != 1 {
				return errors.New("legacy root order changed")
			}
			payload, readErr := os.ReadFile(filepath.Join(roots[0], "auth.json"))
			if readErr != nil {
				return readErr
			}
			if string(payload) != "legacy payload" {
				return errors.New("legacy payload changed")
			}
			roots[0] = "callback-local mutation"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !validBackupAbsolutePath(prepared.roots[0]) {
			t.Fatal("callback changed sealed root order")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *preparedLegacyInputs)
		want   string
	}{
		{
			name: "member bytes",
			mutate: func(t *testing.T, prepared *preparedLegacyInputs) {
				t.Helper()
				mutatePreparedFileBytes(t, prepared.members[0].path, prepared.members[0].info)
			},
			want: "bytes",
		},
		{
			name: "extra inventory",
			mutate: func(t *testing.T, prepared *preparedLegacyInputs) {
				t.Helper()
				writeMigrationFile(t, filepath.Join(prepared.root, "unexpected"), []byte("extra"))
			},
			want: "unexpected",
		},
		{
			name: "outside hard link",
			mutate: func(t *testing.T, prepared *preparedLegacyInputs) {
				t.Helper()
				if err := os.Link(prepared.members[0].path, filepath.Join(t.TempDir(), "alias")); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			},
			want: "link",
		},
		{
			name: "public member",
			mutate: func(t *testing.T, prepared *preparedLegacyInputs) {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("Unix permission assertion")
				}
				if err := os.Chmod(prepared.members[0].path, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "seal changed",
		},
		{
			name: "public root",
			mutate: func(t *testing.T, prepared *preparedLegacyInputs) {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("Unix permission assertion")
				}
				if err := os.Chmod(prepared.root, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "validate",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, spec, session := additionalLiveSnapshot(t)
			prepared, cleanup, err := session.prepareLegacyInputs(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cleanup() })
			test.mutate(t, prepared)
			if err := prepared.guard(t.Context()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("legacy guard after %s = %v", test.name, err)
			}
		})
	}
}

func TestPreparedLegacyUseRechecksAfterCallback(t *testing.T) {
	_, spec, session := additionalLiveSnapshot(t)
	prepared, cleanup, err := session.prepareLegacyInputs(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := prepared.use(t.Context(), func(_ context.Context, roots []string) error {
		mutatePreparedFileBytes(
			t, filepath.Join(roots[0], "auth.json"), prepared.members[0].info,
		)
		return nil
	}); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("mutating legacy callback = %v", err)
	}
}

func TestPreparedLegacyDefensiveOperations(t *testing.T) {
	if err := (*preparedLegacyInputs)(nil).guard(t.Context()); err == nil {
		t.Fatal("nil prepared legacy guard succeeded")
	}
	if err := (*preparedLegacyInputs)(nil).use(t.Context(), func(context.Context, []string) error {
		return nil
	}); err == nil {
		t.Fatal("nil prepared legacy use succeeded")
	}
	if err := (*preparedLegacyInputs)(nil).close(); err != nil {
		t.Fatal(err)
	}
	if err := (&preparedLegacyInputs{}).use(t.Context(), nil); err == nil {
		t.Fatal("nil prepared legacy operation succeeded")
	}
}

func preparedGenerationFixture(t *testing.T, payload []byte) BackupManifest {
	t.Helper()
	manifest := validGenerationManifestFixture(t, "database")
	digest := sha256.Sum256(payload)
	manifest.Files[0].SHA256 = hex.EncodeToString(digest[:])
	manifest.Files[0].Size = int64(len(payload))
	return manifest
}

func mutatePreparedFileBytes(t *testing.T, path string, sealed os.FileInfo) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 {
		t.Fatal("prepared mutation fixture is empty")
	}
	payload[0] ^= 0x20
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, sealed.ModTime(), sealed.ModTime()); err != nil {
		t.Fatal(err)
	}
}
