//go:build unix && !aix

package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFenceAuthorityExpiresWhenLockPathIsReplaced(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lockPath := filepath.Join(home, StateDirectoryName, storageLockFileName)
	if err := os.Rename(lockPath, lockPath+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if fence.Authorizes(home) {
		t.Fatal("fence authorized a replacement lock pathname")
	}
}

func TestFenceAuthorityExpiresWhenStateBoundaryChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "replace", mutate: func(t *testing.T, stateDir string) {
			t.Helper()
			if err := os.Rename(stateDir, stateDir+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(stateDir, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "permissions", mutate: func(t *testing.T, stateDir string) {
			t.Helper()
			if err := os.Chmod(stateDir, 0o777); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Chmod(home, 0o700); err != nil {
				t.Fatal(err)
			}
			fence, err := AcquireOnlineFence(home)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Close()
			test.mutate(t, filepath.Join(home, StateDirectoryName))
			if fence.Authorizes(home) {
				t.Fatal("fence authorized a changed state boundary")
			}
		})
	}
}
