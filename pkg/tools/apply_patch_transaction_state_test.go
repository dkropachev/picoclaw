package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestApplyPatchTransactionStateRootPreparation(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "private-state")
	prepared, err := prepareApplyPatchTransactionStateRoot(workspace, root, nil)
	if err != nil {
		t.Fatalf("prepare state root: %v", err)
	}
	if prepared.path != root {
		t.Fatalf("prepared path = %q, want %q", prepared.path, root)
	}
	if _, statErr := os.Lstat(root); !os.IsNotExist(statErr) {
		t.Fatalf("preparation mutated root: %v", statErr)
	}

	t.Setenv(config.EnvHome, filepath.Join(parent, "first-home"))
	defaultPrepared, err := prepareApplyPatchTransactionStateRoot(workspace, "", nil)
	if err != nil {
		t.Fatalf("prepare default root: %v", err)
	}
	wantDefault := filepath.Join(parent, "first-home", applyPatchTransactionStateDirectory)
	if defaultPrepared.path != wantDefault {
		t.Fatalf("default path = %q, want %q", defaultPrepared.path, wantDefault)
	}
	t.Setenv(config.EnvHome, filepath.Join(parent, "second-home"))
	if defaultPrepared.path != wantDefault {
		t.Fatal("prepared default root followed later configuration mutation")
	}

	invalid := []struct {
		name       string
		workspace  string
		root       string
		allowRoots []string
	}{
		{name: "blank workspace", workspace: "", root: root},
		{name: "workspace root", workspace: workspace, root: workspace},
		{name: "inside workspace", workspace: workspace, root: filepath.Join(workspace, "state")},
		{name: "workspace inside root", workspace: workspace, root: parent},
		{name: "inside allow root", workspace: workspace, root: root, allowRoots: []string{parent}},
		{
			name: "contains allow root", workspace: workspace, root: root,
			allowRoots: []string{filepath.Join(root, "child")},
		},
		{name: "relative allow root", workspace: workspace, root: root, allowRoots: []string{"relative"}},
		{
			name: "unclean allow root", workspace: workspace, root: root,
			allowRoots: []string{filepath.Join(parent, "child", "..")},
		},
		{name: "padded root", workspace: workspace, root: " " + root},
		{name: "nul root", workspace: workspace, root: root + "\x00bad"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, prepareErr := prepareApplyPatchTransactionStateRoot(
				test.workspace,
				test.root,
				test.allowRoots,
			); prepareErr == nil {
				t.Fatal("prepare unexpectedly succeeded")
			}
		})
	}
}

func TestApplyPatchTransactionWorkspaceBindingCodec(t *testing.T) {
	var key [applyPatchTransactionAuthenticationBytes]byte
	for index := range key {
		key[index] = byte(index + 1)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	binding, err := encodeApplyPatchTransactionWorkspaceBinding(workspace, key)
	if err != nil {
		t.Fatalf("encode binding: %v", err)
	}
	decoded, err := decodeApplyPatchTransactionWorkspaceBinding(binding, key)
	if err != nil || decoded != workspace {
		t.Fatalf("decode binding = %q, %v", decoded, err)
	}
	tampered := append([]byte(nil), binding...)
	tampered[len(tampered)-1] ^= 1
	if _, err = decodeApplyPatchTransactionWorkspaceBinding(tampered, key); err == nil {
		t.Fatal("tampered binding was accepted")
	}
	if _, err = decodeApplyPatchTransactionWorkspaceBinding(binding[:4], key); err == nil {
		t.Fatal("short binding was accepted")
	}
	if _, err = encodeApplyPatchTransactionWorkspaceBinding(
		strings.Repeat("x", applyPatchTransactionWorkspacePathLimit+1),
		key,
	); err == nil {
		t.Fatal("oversize workspace was accepted")
	}
	if _, err = encodeApplyPatchTransactionWorkspaceBinding("relative", key); err == nil {
		t.Fatal("relative workspace was accepted")
	}
}

func TestApplyPatchTransactionWorkspaceDigestExact(t *testing.T) {
	workspace := filepath.Join(string(os.PathSeparator), "Case", "workspace")
	want := sha256.Sum256([]byte(workspace))
	if got := applyPatchTransactionWorkspaceDigest(workspace); got != hex.EncodeToString(want[:]) {
		t.Fatalf("digest = %q, want %q", got, hex.EncodeToString(want[:]))
	}
	if got := applyPatchTransactionWorkspaceDigest(workspace); len(got) != sha256.Size*2 {
		t.Fatalf("digest length = %d", len(got))
	}
}

func TestApplyPatchTransactionStateNilAndClosedBoundaries(t *testing.T) {
	var state *applyPatchTransactionState
	if _, err := state.authenticationKey(); err == nil {
		t.Fatal("nil state returned authentication key")
	}
	if _, err := state.authenticationKeyID(); err == nil {
		t.Fatal("nil state returned key ID")
	}
	if _, err := state.rootPath(); err == nil {
		t.Fatal("nil state returned root path")
	}
	if _, err := state.rootIdentity(); err == nil {
		t.Fatal("nil state returned identity")
	}
	if err := state.withRootAnchor(nil); err == nil {
		t.Fatal("nil state accepted rooted operation")
	}
	if err := state.Close(); err != nil {
		t.Fatalf("nil state close: %v", err)
	}

	var workspace *applyPatchTransactionWorkspaceState
	if _, err := workspace.directoryPath(); err == nil {
		t.Fatal("nil workspace returned path")
	}
	if _, err := workspace.directoryRelative(); err == nil {
		t.Fatal("nil workspace returned relative path")
	}
	if err := workspace.withDirectoryAnchor(nil); err == nil {
		t.Fatal("nil workspace accepted rooted operation")
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("nil workspace close: %v", err)
	}
}

func waitForApplyPatchTransactionLockCancellation(
	t *testing.T,
	operation func(context.Context) error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := operation(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock error = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lock cancellation took %v", elapsed)
	}
}
