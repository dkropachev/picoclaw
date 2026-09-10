//go:build unix

package sqliteprovider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStagedMigrationRelativePathsFailWithoutWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	removed := filepath.Join(root, "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}

	ops := stagedMigrationOps{
		replace:  func(string, string) (bool, error) { return true, nil },
		activate: func(context.Context, string, time.Duration, int) error { return nil },
	}
	callback := func(context.Context, string) error { return nil }
	if err := migrateStagedOffline(
		context.Background(), filepath.Join(root, "source.db"), "target.db",
		time.Second, 1, callback, callback, ops,
	); err == nil {
		t.Fatal("relative target resolved without working directory")
	}
	if err := migrateStagedOffline(
		context.Background(), "source.db", filepath.Join(root, "target.db"),
		time.Second, 1, callback, callback, ops,
	); err == nil {
		t.Fatal("relative source resolved without working directory")
	}
}

func TestStagedMigrationRejectsUnsafeModeAfterDomainValidation(t *testing.T) {
	path := createStagedMigrationFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = migrateStagedFixture(
		t.Context(), path, time.Second, 1,
		installStagedFixtureTable,
		func(_ context.Context, stage string) error { return os.Chmod(stage, 0o400) },
	)
	if err == nil {
		t.Fatal("post-validation unsafe mode reached cutover")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("post-validation unsafe mode changed live target")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("live target security = %v, %v", info, err)
	}
}
