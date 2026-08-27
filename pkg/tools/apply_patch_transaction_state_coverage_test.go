package tools

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionStatePrivatePublicationCoverage(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	root, openErr := os.OpenRoot(directory)
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = root.Close() })

	if err := cleanupApplyPatchTransactionPrivateStage(
		root, directory, "missing", []byte("x"), false,
	); err != nil {
		t.Fatalf("missing stage cleanup = %v", err)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		nil, directory, "value", []byte("value"),
	); err == nil {
		t.Fatal("nil private publication succeeded")
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, "../value", []byte("value"),
	); err == nil {
		t.Fatal("unsafe private publication succeeded")
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, "value", []byte("value"),
	); err != nil {
		t.Fatalf("private publication = %v", err)
	}
	data, _, readErr := readApplyPatchTransactionPrivateRegular(root, "value", len("value"))
	if readErr != nil || !bytes.Equal(data, []byte("value")) {
		t.Fatalf("published private data = %q, %v", data, readErr)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, "value", []byte("other"),
	); err != nil {
		t.Fatalf("create-only loser cleanup = %v", err)
	}
	data, _, readErr = readApplyPatchTransactionPrivateRegular(root, "value", len("value"))
	if readErr != nil || !bytes.Equal(data, []byte("value")) {
		t.Fatalf("create-only winner changed = %q, %v", data, readErr)
	}

	authStage := filepath.Join(
		directory,
		"."+applyPatchTransactionAuthenticationFile+".stage",
	)
	if err := os.WriteFile(authStage, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := bytes.Repeat([]byte{7}, applyPatchTransactionAuthenticationBytes)
	if err := publishApplyPatchTransactionPrivateRegular(
		root,
		directory,
		applyPatchTransactionAuthenticationFile,
		auth,
	); err != nil {
		t.Fatalf("stale authentication stage recovery = %v", err)
	}
	gotAuth, _, authReadErr := readApplyPatchTransactionPrivateRegular(
		root,
		applyPatchTransactionAuthenticationFile,
		len(auth),
	)
	if authReadErr != nil || !bytes.Equal(gotAuth, auth) {
		t.Fatalf("authentication winner = %x, %v", gotAuth, authReadErr)
	}

	if err := os.WriteFile(filepath.Join(directory, ".conflict.stage"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, "conflict", []byte("longer"),
	); err == nil {
		t.Fatal("mismatched ordinary stage was accepted")
	}
}

func TestApplyPatchTransactionStatePrivateValidationCoverage(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	root, openErr := os.OpenRoot(directory)
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = root.Close() })

	if err := writeApplyPatchTransactionSyncedFile(nil, []byte("x")); err == nil {
		t.Fatal("nil synced file succeeded")
	}
	if _, _, err := readApplyPatchTransactionPrivateRegular(nil, "x", 1); err == nil {
		t.Fatal("nil private read succeeded")
	}
	if _, _, err := readApplyPatchTransactionPrivateRegular(root, "x", -1); err == nil {
		t.Fatal("negative private read succeeded")
	}
	if _, _, err := readApplyPatchTransactionPrivateRegular(root, "missing", 1); err == nil {
		t.Fatal("missing private read succeeded")
	}
	if err := root.Mkdir("directory", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readApplyPatchTransactionPrivateRegular(root, "directory", 0); err == nil {
		t.Fatal("directory private read succeeded")
	}
	if err := os.WriteFile(filepath.Join(directory, "file"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileInfo, statErr := root.Lstat("file")
	if statErr != nil {
		t.Fatal(statErr)
	}
	if _, _, err := readApplyPatchTransactionPrivateRegular(root, "file", 3); err == nil {
		t.Fatal("wrong-length private read succeeded")
	}
	if err := revalidateApplyPatchTransactionRegular(nil, "file", fileInfo); err == nil {
		t.Fatal("nil regular revalidation succeeded")
	}
	if err := revalidateApplyPatchTransactionRegular(root, "file", nil); err == nil {
		t.Fatal("nil identity revalidation succeeded")
	}
	if err := revalidateApplyPatchTransactionRegular(root, "file", fileInfo); err != nil {
		t.Fatalf("regular revalidation = %v", err)
	}
	if err := os.Chmod(filepath.Join(directory, "file"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchTransactionRegular(root, "file", fileInfo); err == nil {
		t.Fatal("mode drift passed regular revalidation")
	}

	if err := removeApplyPatchTransactionExactRootEntry(nil, "file", fileInfo); err == nil {
		t.Fatal("nil exact removal succeeded")
	}
	if err := removeApplyPatchTransactionExactRootEntry(root, "file", nil); err == nil {
		t.Fatal("identity-less exact removal succeeded")
	}
	otherPath := filepath.Join(directory, "other")
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherInfo, otherStatErr := root.Lstat("other")
	if otherStatErr != nil {
		t.Fatal(otherStatErr)
	}
	if err := removeApplyPatchTransactionExactRootEntry(root, "file", otherInfo); err == nil {
		t.Fatal("wrong-identity exact removal succeeded")
	}
	if err := removeApplyPatchTransactionExactRootEntry(root, "other", otherInfo); err != nil {
		t.Fatalf("exact removal = %v", err)
	}
	if _, err := os.Lstat(otherPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact removal residue = %v", err)
	}
}

func TestApplyPatchTransactionStateFenceRevalidationCoverage(t *testing.T) {
	if err := revalidateApplyPatchTransactionStateFences(
		applyPatchTransactionStateRoot{},
	); err == nil {
		t.Fatal("empty state fences revalidated")
	}
	parent := t.TempDir()
	statePath := filepath.Join(parent, "state", "future")
	fences, captureErr := captureApplyPatchTransactionStateFences(statePath)
	if captureErr != nil {
		t.Fatal(captureErr)
	}
	prepared := applyPatchTransactionStateRoot{path: statePath, fences: fences}
	if err := revalidateApplyPatchTransactionStateFences(prepared); err != nil {
		t.Fatalf("stable fences = %v", err)
	}
	if err := os.Chmod(parent, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchTransactionStateFences(prepared); err == nil {
		t.Fatal("fence mode drift was accepted")
	}
}
