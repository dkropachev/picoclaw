//go:build linux

package tools

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/logger"
)

func TestToolRegistryRegistrationAndPromotionDiagnosticsReleaseRegistryLock(t *testing.T) {
	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	logger.DisableFileLogging()
	defer func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	}()

	t.Run("registration", func(t *testing.T) {
		registry := NewToolRegistry()
		assertRegistryDiagnosticWriterReentry(
			t,
			registry,
			"(*ToolRegistry).registerLegacy",
			func() { registry.Register(newMockTool("reentry_register", "reentry")) },
		)
	})
	t.Run("promotion", func(t *testing.T) {
		registry := NewToolRegistry()
		registry.RegisterHidden(newMockTool("reentry_promote", "reentry"))
		assertRegistryDiagnosticWriterReentry(
			t,
			registry,
			"(*ToolRegistry).PromoteTools",
			func() { registry.PromoteTools([]string{"reentry_promote"}, 2) },
		)
	})
}

// assertRegistryDiagnosticWriterReentry fills a FIFO sink completely, waits
// until the registry diagnostic goroutine is blocked inside the actual file
// writer, then re-enters the registry before allowing that write to finish.
// If emission still owns r.mu, Count cannot return and the test times out.
func assertRegistryDiagnosticWriterReentry(
	t *testing.T,
	registry *ToolRegistry,
	stackNeedle string,
	emit func(),
) {
	t.Helper()
	logger.DisableFileLogging()
	fifoPath := filepath.Join(t.TempDir(), "registry-log.fifo")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}
	anchor, err := os.OpenFile(fifoPath, os.O_RDWR|unix.O_NONBLOCK, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(FIFO) error = %v", err)
	}
	defer func() { _ = anchor.Close() }()
	if err := logger.EnableFileLogging(fifoPath); err != nil {
		t.Fatalf("EnableFileLogging(FIFO) error = %v", err)
	}
	defer logger.DisableFileLogging()
	fillRegistryDiagnosticPipe(t, int(anchor.Fd()))

	emitted := make(chan struct{})
	go func() {
		defer close(emitted)
		emit()
	}()
	waitForRegistryDiagnosticWrite(t, emitted, stackNeedle)

	reentered := make(chan struct{})
	go func() {
		_ = registry.Count()
		close(reentered)
	}()
	select {
	case <-reentered:
		// The registry lock is free while the real sink remains blocked.
	case <-time.After(time.Second):
		drainRegistryDiagnosticPipe(t, int(anchor.Fd()))
		<-emitted
		<-reentered
		t.Fatal("diagnostic writer reentry blocked on registry mutex")
	}

	drainRegistryDiagnosticPipe(t, int(anchor.Fd()))
	select {
	case <-emitted:
	case <-time.After(time.Second):
		t.Fatal("registry diagnostic did not finish after FIFO drain")
	}
}

func fillRegistryDiagnosticPipe(t *testing.T, descriptor int) {
	t.Helper()
	buffer := make([]byte, 4096)
	for size := len(buffer); size >= 1; size /= 2 {
		for {
			written, err := unix.Write(descriptor, buffer[:size])
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				break
			}
			if err != nil {
				t.Fatalf("fill FIFO write error = %v", err)
			}
			if written != size {
				t.Fatalf("fill FIFO short write = %d, want %d", written, size)
			}
		}
	}
}

func drainRegistryDiagnosticPipe(t *testing.T, descriptor int) {
	t.Helper()
	buffer := make([]byte, 64<<10)
	read, err := unix.Read(descriptor, buffer)
	if err != nil {
		t.Fatalf("drain FIFO read error = %v", err)
	}
	if read == 0 {
		t.Fatal("drain FIFO read no bytes")
	}
}

func waitForRegistryDiagnosticWrite(
	t *testing.T,
	emitted <-chan struct{},
	stackNeedle string,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	stack := make([]byte, 2<<20)
	for time.Now().Before(deadline) {
		select {
		case <-emitted:
			t.Fatal("diagnostic emission completed despite a full FIFO")
		default:
		}
		count := runtime.Stack(stack, true)
		active := stack[:count]
		if bytes.Contains(active, []byte(stackNeedle)) &&
			bytes.Contains(active, []byte("os.(*File).Write")) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	count := runtime.Stack(stack, true)
	t.Fatalf(
		"did not observe blocked registry diagnostic writer for %q\n%s",
		stackNeedle,
		stack[:min(count, 32<<10)],
	)
}
