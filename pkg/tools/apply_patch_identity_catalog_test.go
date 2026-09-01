package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchRejectsCatalogAliasAfterSourceArchiveRename(t *testing.T) {
	workspace := t.TempDir()
	active := filepath.Join(t.TempDir(), "active.json")
	if err := os.WriteFile(active, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{ExactPaths: []string{active}})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(workspace, "runtime-alias.json")
	if linkErr := os.Link(active, alias); linkErr != nil {
		t.Skipf("hardlinks unavailable: %v", linkErr)
	}
	archive := filepath.Join(filepath.Dir(active), "legacy-json", "component-v1", "active.json")
	if mkdirErr := os.MkdirAll(filepath.Dir(archive), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if renameErr := os.Rename(active, archive); renameErr != nil {
		t.Fatal(renameErr)
	}
	transactionRoot := filepath.Join(t.TempDir(), "transactions")
	tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		true,
		true,
		true,
		ApplyPatchPreflightPolicy{
			ProtectedIdentities:  catalog,
			TransactionStateRoot: transactionRoot,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(context.Background(), map[string]any{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: runtime-alias.json",
		"@@",
		"-before",
		"+changed",
		"*** End Patch",
	}, "\n")})
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "protected") {
		t.Fatalf("catalog alias patch = %#v", result)
	}
	content, readErr := os.ReadFile(archive)
	if readErr != nil || string(content) != "before\n" {
		t.Fatalf("archived identity = %q, %v", content, readErr)
	}
}

func TestApplyPatchChecksCatalogOnActualSourceHandleBeforeReading(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "source.txt")
	protected := filepath.Join(t.TempDir(), "runtime-private.json")
	const secret = "never disclose protected runtime bytes\n"
	if err := os.WriteFile(source, []byte("ordinary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protected, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{protected},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		true,
		true,
		true,
		ApplyPatchPreflightPolicy{
			ProtectedIdentities:  catalog,
			TransactionStateRoot: filepath.Join(t.TempDir(), "transactions"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	tool.beforeSourceOpen = func(string) {
		tool.beforeSourceOpen = nil
		if removeErr := os.Remove(source); removeErr != nil {
			t.Fatal(removeErr)
		}
		if renameErr := os.Rename(protected, source); renameErr != nil {
			t.Fatal(renameErr)
		}
	}
	result := tool.Execute(context.Background(), map[string]any{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: source.txt",
		"@@",
		"-ordinary",
		"+changed",
		"*** End Patch",
	}, "\n")})
	if result == nil || !result.IsError || strings.Contains(result.ForLLM, secret) ||
		strings.Contains(result.ForUser, secret) {
		t.Fatalf("swap-raced catalog patch = %#v", result)
	}
	content, readErr := os.ReadFile(source)
	if readErr != nil || string(content) != secret {
		t.Fatalf("protected source after rejected patch = %q, %v", content, readErr)
	}
}
