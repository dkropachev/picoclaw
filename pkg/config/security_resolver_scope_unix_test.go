//go:build unix && !aix

package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestConcurrentConfigLoadsKeepCredentialResolversDirectoryScoped(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvHome, filepath.Join(root, "home"))
	firstDirectory := filepath.Join(root, "first")
	secondDirectory := filepath.Join(root, "second")
	if err := os.MkdirAll(firstDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstDirectory, "value.key"), []byte("first-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondDirectory, "value.key"), []byte("second-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstPath := writeResolverScopeConfig(
		t,
		firstDirectory,
		"file://blocking.key",
		"file://value.key",
	)
	secondPath := writeResolverScopeConfig(t, secondDirectory, "file://value.key")
	fifoPath := filepath.Join(firstDirectory, "blocking.key")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan configSnapshotDriftResult, 1)
	go func() {
		cfg, err := LoadConfig(firstPath)
		firstDone <- configSnapshotDriftResult{config: cfg, err: err}
	}()
	writer := awaitConfigSnapshotFIFOReader(t, fifoPath, firstDone)
	released := false
	t.Cleanup(func() {
		if !released {
			_, _ = writer.WriteString("cleanup-secret\n")
			_ = writer.Close()
		}
	})

	secondStarted := make(chan struct{})
	secondDone := make(chan resolverScopeLoadResult, 1)
	go func() {
		close(secondStarted)
		cfg, err := LoadConfigForUpdate(secondPath)
		secondDone <- resolverScopeLoadResult{config: cfg, err: err}
	}()
	<-secondStarted
	select {
	case result := <-secondDone:
		t.Fatalf("second directory load bypassed resolver scope: %#v", result)
	case <-time.After(200 * time.Millisecond):
	}

	if _, err := writer.WriteString("first-fifo\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	released = true

	var first configSnapshotDriftResult
	select {
	case first = <-firstDone:
		if first.err != nil || first.config == nil {
			t.Fatalf("first config load = %#v, %v", first.config, first.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first config load did not finish")
	}
	second := awaitResolverScopeLoad(t, secondDone)
	firstValues := resolverScopeModelKeys(first.config)
	if !slices.Contains(firstValues, "first-fifo") || !slices.Contains(firstValues, "first-local") ||
		slices.Contains(firstValues, "second-local") {
		t.Fatalf("first load resolved keys from wrong directory: %#v", firstValues)
	}
	secondValues := resolverScopeModelKeys(second.config)
	if !slices.Equal(secondValues, []string{"second-local"}) {
		t.Fatalf("second load resolved keys from wrong directory: %#v", secondValues)
	}
}
