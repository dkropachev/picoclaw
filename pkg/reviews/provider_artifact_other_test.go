//go:build !linux && !windows && !js && !plan9

package reviews

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableProviderArtifactConsumeRemovesAfterReadFailure(t *testing.T) {
	root := privateProviderArtifactRoot(t)
	path := filepath.Join(root, "provider.json")
	writeProviderArtifact(t, path, `{"ok":true}`)
	artifact, err := acquireProviderArtifact(root, path, nil)
	if err != nil {
		t.Fatalf("acquireProviderArtifact() error = %v", err)
	}
	if err := artifact.File.Close(); err != nil {
		t.Fatalf("close artifact early: %v", err)
	}
	if _, err := artifact.File.Read(make([]byte, 1)); err == nil {
		t.Fatal("read from closed artifact unexpectedly succeeded")
	}
	if err := artifact.Consume(); err == nil {
		t.Fatal("Consume() unexpectedly hid the closed-file error")
	}
	assertProviderArtifactRemoved(t, path)
}

func TestPortableProviderArtifactConsumePreservesRacedReplacement(t *testing.T) {
	root := privateProviderArtifactRoot(t)
	path := filepath.Join(root, "provider.json")
	moved := filepath.Join(root, "original.json")
	writeProviderArtifact(t, path, `{"original":true}`)
	artifact, err := acquireProviderArtifact(root, path, nil)
	if err != nil {
		t.Fatalf("acquireProviderArtifact() error = %v", err)
	}
	if err := artifact.File.Close(); err != nil {
		t.Fatalf("close artifact before race: %v", err)
	}
	artifact.File = nil
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("move acquired artifact: %v", err)
	}
	writeProviderArtifact(t, path, `{"replacement":true}`)
	if err := artifact.Consume(); err == nil ||
		!strings.Contains(err.Error(), "changed before cleanup") {
		t.Fatalf("Consume() error = %v, want changed-before-cleanup error", err)
	}
	assertProviderArtifactContents(t, path, `{"replacement":true}`)
	assertProviderArtifactContents(t, moved, `{"original":true}`)
}

func TestPortableProviderArtifactRejectsSymlinkedParent(t *testing.T) {
	root := privateProviderArtifactRoot(t)
	outside := privateProviderArtifactRoot(t)
	outsidePath := filepath.Join(outside, "outside.json")
	writeProviderArtifact(t, outsidePath, `{"outside":true}`)
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}
	artifact, err := acquireProviderArtifact(
		root,
		filepath.Join(link, "outside.json"),
		nil,
	)
	if artifact != nil {
		_ = artifact.Consume()
		t.Fatal("acquireProviderArtifact() returned an artifact through a symlinked parent")
	}
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acquireProviderArtifact() error = %v, want symlink rejection", err)
	}
	assertProviderArtifactContents(t, outsidePath, `{"outside":true}`)
}
