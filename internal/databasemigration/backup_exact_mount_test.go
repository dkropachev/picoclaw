package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExactBackupMountIdentityComparison(t *testing.T) {
	root := exactBackupMountIdentity{
		mechanism: "test-mount", value: [2]uint64{7, 11}, mountPoint: "/backup",
	}
	if err := validateExactBackupMountIdentity(root, root); err != nil {
		t.Fatalf("same mount rejected: %v", err)
	}

	for _, test := range []struct {
		name  string
		child exactBackupMountIdentity
		want  error
	}{
		{name: "mount id", child: exactBackupMountIdentity{
			mechanism: "test-mount", value: [2]uint64{8, 11}, mountPoint: "/backup",
		}, want: errExactBackupMountBoundary},
		{name: "same filesystem bind", child: exactBackupMountIdentity{
			mechanism: "test-mount", value: [2]uint64{7, 11}, mountPoint: "/backup/bind",
		}, want: errExactBackupMountBoundary},
		{name: "missing identity", want: errExactBackupMountUnknown},
		{name: "zero identity", child: exactBackupMountIdentity{
			mechanism: "test-mount", mountPoint: "/backup",
		}, want: errExactBackupMountUnknown},
		{name: "incomparable identity", child: exactBackupMountIdentity{
			mechanism: "other-mount", value: [2]uint64{7, 11}, mountPoint: "/backup",
		}, want: errExactBackupMountUnknown},
		{name: "missing BSD mount point", child: exactBackupMountIdentity{
			mechanism: "bsd-fstatfs", value: [2]uint64{7, 11},
		}, want: errExactBackupMountUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateExactBackupMountIdentity(root, test.child); !errors.Is(err, test.want) {
				t.Fatalf("mount identity comparison = %v; want %v", err, test.want)
			}
		})
	}
}

func TestExactBackupMountNameStopsAtNUL(t *testing.T) {
	if got, ok := exactBackupMountName([]byte{'/', 'd', 'b', 0, 'x'}); !ok || got != "/db" {
		t.Fatalf("mount name = %q, %t", got, ok)
	}
	if got, ok := exactBackupMountName([]byte("/whole")); ok || got != "" {
		t.Fatalf("unterminated mount name = %q, %t", got, ok)
	}
	if got, ok := exactBackupMountName([]byte{0, 'x'}); ok || got != "" {
		t.Fatalf("empty mount name = %q, %t", got, ok)
	}
}

func TestOpenExactBackupChildByMountIdentity(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "file"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	identity := exactBackupMountIdentity{mechanism: "test-mount", value: [2]uint64{19}}
	file, err := openExactBackupChildByMountIdentity(
		root, "file", func(*os.File) (exactBackupMountIdentity, error) { return identity, nil },
	)
	if err != nil || file == nil {
		t.Fatalf("same-mount child = %#v, %v", file, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	var rejected *os.File
	calls := 0
	file, err = openExactBackupChildByMountIdentity(
		root,
		"file",
		func(opened *os.File) (exactBackupMountIdentity, error) {
			calls++
			if calls == 2 {
				rejected = opened
				return exactBackupMountIdentity{
					mechanism: "test-mount", value: [2]uint64{20},
				}, nil
			}
			return identity, nil
		},
	)
	if file != nil || !errors.Is(err, errExactBackupMountBoundary) {
		t.Fatalf("different-mount child = %#v, %v", file, err)
	}
	if rejected == nil {
		t.Fatal("rejected child was not inspected")
	}
	if _, statErr := rejected.Stat(); !errors.Is(statErr, os.ErrClosed) {
		t.Fatalf("rejected child close = %v", statErr)
	}
}
