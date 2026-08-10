package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
)

func TestNewPRDevelopmentLocalCIRuntimeUsesPrivateEventState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("production local CI sandbox is Linux-only")
	}
	workspace := t.TempDir()
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.DatabasePath = filepath.Join("state", "events.db")

	ciRuntime, err := newPRDevelopmentLocalCIRuntime(cfg)
	if err != nil {
		if errorsIsSandboxUnavailable(err) {
			t.Skipf("production local CI backend unavailable: %v", err)
		}
		t.Fatalf("newPRDevelopmentLocalCIRuntime() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := ciRuntime.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})
	if ciRuntime.runner == nil || ciRuntime.runner.Store == nil || ciRuntime.runner.Sandbox == nil {
		t.Fatalf("local CI runtime = %#v", ciRuntime)
	}
	root := filepath.Join(workspace, "state", prDevelopmentLocalCIDirectory)
	for _, directory := range []string{root, filepath.Join(root, "tmp"), filepath.Join(root, "evidence")} {
		info, statErr := os.Lstat(directory)
		if statErr != nil {
			t.Fatalf("Lstat(%q) error = %v", directory, statErr)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("directory %q mode = %s", directory, info.Mode())
		}
	}
}

func TestEnsurePrivatePRDevelopmentDirectoryRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ensurePrivatePRDevelopmentDirectory(link); err == nil {
		t.Fatal("ensurePrivatePRDevelopmentDirectory(symlink) error = nil")
	}
}

func errorsIsSandboxUnavailable(err error) bool {
	return err != nil && (os.IsNotExist(err) || errors.Is(err, localci.ErrSandboxUnavailable))
}
