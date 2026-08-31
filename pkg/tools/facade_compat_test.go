package tools

import (
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
	policy := FileMutationPolicy{ProtectedRoots: []string{"runtime-state"}}
	if tool, err := NewWriteFileToolWithPolicy(workspace, true, policy); err != nil || tool == nil {
		t.Fatalf("NewWriteFileToolWithPolicy() = %#v, %v", tool, err)
	}
	if tool, err := NewEditFileToolWithPolicy(workspace, true, policy); err != nil || tool == nil {
		t.Fatalf("NewEditFileToolWithPolicy() = %#v, %v", tool, err)
	}
	if tool, err := NewAppendFileToolWithPolicy(workspace, true, policy); err != nil || tool == nil {
		t.Fatalf("NewAppendFileToolWithPolicy() = %#v, %v", tool, err)
	}
}
