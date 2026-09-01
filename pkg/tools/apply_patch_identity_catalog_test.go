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
