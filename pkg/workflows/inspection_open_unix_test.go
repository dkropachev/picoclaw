//go:build unix

package workflows

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOpenWorkflowInspectionDefinitionDoesNotBlockOnFIFO(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	fifoPath := filepath.Join(rootPath, "definition.yml")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("Mkfifo() unavailable: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	defer root.Close()

	result := make(chan error, 1)
	go func() {
		file, openErr := openWorkflowInspectionDefinition(root, "definition.yml")
		if openErr == nil {
			if info, statErr := file.Stat(); statErr != nil {
				openErr = statErr
			} else if info.Mode().IsRegular() {
				openErr = ErrWorkflowInspectionUnavailable
			}
			_ = file.Close()
		}
		result <- openErr
	}()

	select {
	case openErr := <-result:
		if openErr != nil {
			t.Fatalf("nonblocking FIFO open error = %v", openErr)
		}
	case <-time.After(time.Second):
		t.Fatal("root-confined FIFO open blocked")
	}
}
