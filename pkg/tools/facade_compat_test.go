package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/skills"
)

func TestFacadeConstructorsRemainAvailable(t *testing.T) {
	if NewI2CTool() == nil {
		t.Fatal("NewI2CTool should return a tool")
	}
	if NewSPITool() == nil {
		t.Fatal("NewSPITool should return a tool")
	}
	if NewSerialTool() == nil {
		t.Fatal("NewSerialTool should return a tool")
	}
	if NewMessageTool() == nil {
		t.Fatal("NewMessageTool should return a tool")
	}
	if NewInstallSkillToolWithLock(
		skills.NewRegistryManager(),
		t.TempDir(),
		&sync.Mutex{},
	) == nil {
		t.Fatal("NewInstallSkillToolWithLock should return a tool")
	}
}

func TestFileMutationPolicyFacadeConstructorsRemainAvailable(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "runtime-state")
	if err := os.WriteFile(protected, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, catalogErr := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{protected},
	})
	if catalogErr != nil {
		t.Fatal(catalogErr)
	}
	prepared, prepareErr := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedRoots:      []string{protected},
		ProtectedIdentities: catalog,
	})
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	policy := FileMutationPolicy{Prepared: prepared}
	if tool, err := NewWriteFileToolWithPolicy(workspace, true, policy); err != nil || tool == nil {
		t.Fatalf("NewWriteFileToolWithPolicy() = %#v, %v", tool, err)
	}
	if tool, err := NewEditFileToolWithPolicy(workspace, true, policy); err != nil || tool == nil {
		t.Fatalf("NewEditFileToolWithPolicy() = %#v, %v", tool, err)
	}
	if tool, err := NewAppendFileToolWithPolicy(workspace, true, policy); err != nil || tool == nil {
		t.Fatalf("NewAppendFileToolWithPolicy() = %#v, %v", tool, err)
	}
	readTool, err := NewReadFileLinesToolWithPolicy(
		workspace, true, MaxReadFileSize, policy,
	)
	if err != nil || readTool == nil {
		t.Fatalf("NewReadFileLinesToolWithPolicy() = %#v, %v", readTool, err)
	}
	readResult := readTool.Execute(context.Background(), map[string]any{"path": protected})
	if readResult == nil || !readResult.IsError ||
		!strings.Contains(readResult.ForLLM, "protected runtime state") {
		t.Fatalf("facade protected read result = %#v", readResult)
	}
	listTool, err := NewListDirToolWithPolicy(workspace, true, policy)
	if err != nil || listTool == nil {
		t.Fatalf("NewListDirToolWithPolicy() = %#v, %v", listTool, err)
	}
	listResult := listTool.Execute(context.Background(), map[string]any{"path": protected})
	if listResult == nil || !listResult.IsError ||
		!strings.Contains(listResult.ForLLM, "protected runtime state") {
		t.Fatalf("facade protected list result = %#v", listResult)
	}
}
