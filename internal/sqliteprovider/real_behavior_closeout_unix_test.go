//go:build unix

package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRealBehaviorProviderRejectsUnsearchableStorageBoundary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory search permissions")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Errorf("restore test directory: %v", err)
		}
	}
	t.Cleanup(restore)

	path := filepath.Join(root, "store.db")
	_, inspectErr := Inspect(t.Context(), path, time.Second)
	_, generationErr := regularGenerationExists(path)
	sidecarErr := requireNoGenerationSidecars(path)
	_, stageNameErr := unusedStagedGenerationPath(path)
	migrationErr := migrateStagedFixture(
		context.Background(),
		filepath.Join(root, "nested", "store.db"),
		time.Second,
		1,
		func(context.Context, string) error { return nil },
		acceptStagedValidation,
	)
	restore()

	for name, err := range map[string]error{
		"inspection":       inspectErr,
		"generation stat":  generationErr,
		"sidecar stat":     sidecarErr,
		"stage allocation": stageNameErr,
		"migration parent": migrationErr,
	} {
		if !errors.Is(err, os.ErrPermission) {
			t.Errorf("%s error = %v, want permission denial", name, err)
		}
	}
}
