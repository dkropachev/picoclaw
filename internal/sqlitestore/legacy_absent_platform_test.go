//go:build (unix && !aix) || windows

package sqlitestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func TestSealedAbsentLegacyRootLifecycle(t *testing.T) {
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	path := filepath.Join(ancestor, "missing-parent", "legacy-root")
	before, err := os.Lstat(ancestor)
	if err != nil {
		t.Fatal(err)
	}

	proof, err := captureSealedAbsentLegacyRoot(t.Context(), path)
	if err != nil {
		t.Fatalf("captureSealedAbsentLegacyRoot() error = %v", err)
	}
	if proof.path != path || proof.ancestorPath != ancestor ||
		!reflect.DeepEqual(proof.suffix, []string{"missing-parent", "legacy-root"}) {
		t.Fatalf(
			"sealed absence layout = path:%q ancestor:%q suffix:%q",
			proof.path,
			proof.ancestorPath,
			proof.suffix,
		)
	}
	if revalidateErr := revalidateSealedAbsentLegacyRoot(t.Context(), proof); revalidateErr != nil {
		t.Fatalf("revalidateSealedAbsentLegacyRoot() error = %v", revalidateErr)
	}
	if consumedErr := requireSealedAbsentLegacyRootConsumed(proof); consumedErr == nil {
		t.Fatal("fresh sealed absence proof reported consumed")
	}
	if commitErr := checkSealedAbsentLegacyRootBeforeCommit(t.Context(), proof); commitErr == nil {
		t.Fatal("sealed absence proof skipped empty-enumeration mark")
	}
	if markErr := markSealedAbsentLegacyRootEnumerated(t.Context(), proof); markErr != nil {
		t.Fatalf("markSealedAbsentLegacyRootEnumerated() error = %v", markErr)
	}
	if markErr := markSealedAbsentLegacyRootEnumerated(t.Context(), proof); markErr == nil {
		t.Fatal("sealed absence proof accepted a second enumeration mark")
	}
	if consumedErr := requireSealedAbsentLegacyRootConsumed(proof); consumedErr == nil {
		t.Fatal("enumerated sealed absence proof reported consumed before precommit")
	}
	if commitErr := checkSealedAbsentLegacyRootBeforeCommit(t.Context(), proof); commitErr != nil {
		t.Fatalf("checkSealedAbsentLegacyRootBeforeCommit() error = %v", commitErr)
	}
	if commitErr := checkSealedAbsentLegacyRootBeforeCommit(t.Context(), proof); commitErr == nil {
		t.Fatal("sealed absence proof accepted a second precommit check")
	}
	if consumedErr := requireSealedAbsentLegacyRootConsumed(proof); consumedErr != nil {
		t.Fatalf("requireSealedAbsentLegacyRootConsumed() error = %v", consumedErr)
	}
	if revalidateErr := revalidateSealedAbsentLegacyRoot(t.Context(), proof); revalidateErr == nil {
		t.Fatal("consumed sealed absence proof accepted a generic revalidation")
	}
	after, err := os.Lstat(ancestor)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatalf("proof changed ancestor = %#v/%#v, %v", before, after, err)
	}
	entries, err := os.ReadDir(ancestor)
	if err != nil || len(entries) != 0 {
		t.Fatalf("proof mutated absent namespace = %#v, %v", entries, err)
	}
	if err := closeSealedAbsentLegacyRoot(proof); err != nil {
		t.Fatalf("closeSealedAbsentLegacyRoot() error = %v", err)
	}
	if err := closeSealedAbsentLegacyRoot(proof); err != nil {
		t.Fatalf("second closeSealedAbsentLegacyRoot() error = %v", err)
	}
	if err := closeSealedAbsentLegacyRoot(nil); err != nil {
		t.Fatalf("nil closeSealedAbsentLegacyRoot() error = %v", err)
	}
	if err := revalidateSealedAbsentLegacyRoot(t.Context(), proof); err == nil {
		t.Fatal("closed sealed absence proof revalidated")
	}
}

func TestSealedAbsentLegacyRootRejectsExistingRootAndLaterAppearance(t *testing.T) {
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	for _, test := range []struct {
		name   string
		create func(string) error
	}{
		{name: "directory", create: func(path string) error { return os.Mkdir(path, 0o700) }},
		{name: "regular", create: func(path string) error {
			return os.WriteFile(path, []byte("present"), 0o600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(ancestor, "initial-"+test.name)
			if err := test.create(path); err != nil {
				t.Fatal(err)
			}
			if proof, err := captureSealedAbsentLegacyRoot(t.Context(), path); proof != nil || err == nil {
				if proof != nil {
					_ = closeSealedAbsentLegacyRoot(proof)
				}
				t.Fatalf("existing %s root = %#v, %v", test.name, proof, err)
			}

			missing := filepath.Join(ancestor, "late-"+test.name, "root")
			proof, err := captureSealedAbsentLegacyRoot(t.Context(), missing)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = closeSealedAbsentLegacyRoot(proof) })
			if err := test.create(filepath.Join(ancestor, "late-"+test.name)); err != nil {
				t.Fatal(err)
			}
			if err := revalidateSealedAbsentLegacyRoot(t.Context(), proof); err == nil {
				t.Fatalf("sealed absence accepted later %s appearance", test.name)
			}
		})
	}
}

func TestSealedAbsentLegacyRootRejectsSymlinkAppearance(t *testing.T) {
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	target := filepath.Join(ancestor, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ancestor, "late-link", "root")
	proof, err := captureSealedAbsentLegacyRoot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSealedAbsentLegacyRoot(proof)
	if err := os.Symlink(target, filepath.Join(ancestor, "late-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := revalidateSealedAbsentLegacyRoot(t.Context(), proof); err == nil {
		t.Fatal("sealed absence accepted a later symlink or reparse point")
	}
}

func TestSealedAbsentLegacyRootContextAndInputValidation(t *testing.T) {
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	path := filepath.Join(ancestor, "missing")
	if proof, err := captureSealedAbsentLegacyRoot(nil, path); proof != nil || err == nil {
		t.Fatalf("nil-context capture = %#v, %v", proof, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if proof, err := captureSealedAbsentLegacyRoot(canceled, path); proof != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture = %#v, %v", proof, err)
	}
	for _, invalid := range []string{
		"",
		"relative",
		ancestor + string(os.PathSeparator) + "nested" + string(os.PathSeparator) + ".." +
			string(os.PathSeparator) + "missing",
		path + string(os.PathSeparator),
		path + "\x00suffix",
	} {
		if proof, err := captureSealedAbsentLegacyRoot(t.Context(), invalid); proof != nil || err == nil {
			if proof != nil {
				_ = closeSealedAbsentLegacyRoot(proof)
			}
			t.Errorf("invalid path %q = %#v, %v", invalid, proof, err)
		}
	}
	if proof, err := captureSealedAbsentLegacyRoot(t.Context(), ancestor); proof != nil || err == nil {
		if proof != nil {
			_ = closeSealedAbsentLegacyRoot(proof)
		}
		t.Fatalf("existing ancestor as absent root = %#v, %v", proof, err)
	}

	proof, err := captureSealedAbsentLegacyRoot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSealedAbsentLegacyRoot(proof)
	if err := revalidateSealedAbsentLegacyRoot(nil, proof); err == nil {
		t.Fatal("nil-context revalidation succeeded")
	}
	if err := revalidateSealedAbsentLegacyRoot(canceled, proof); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled revalidation error = %v", err)
	}
	if err := revalidateSealedAbsentLegacyRoot(t.Context(), nil); err == nil {
		t.Fatal("nil proof revalidation succeeded")
	}
	malformed := &sealedAbsentLegacyRoot{path: path, ancestorPath: ancestor}
	if err := revalidateSealedAbsentLegacyRoot(t.Context(), malformed); err == nil {
		t.Fatal("malformed proof revalidation succeeded")
	}
}

func privateSealedAbsentLegacyTestDirectory(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if _, err := fileutil.SecurePrivateDirectory(path); err != nil {
		t.Fatalf("secure sealed absence test directory: %v", err)
	}
	return filepath.Clean(path)
}
