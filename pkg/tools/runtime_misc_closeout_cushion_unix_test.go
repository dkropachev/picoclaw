//go:build !windows

package tools

import (
	"os"
	"testing"
)

func TestRuntimeMiscCloseoutCushionPTYErrors(t *testing.T) {
	if replacement, err := makePTYMasterInterruptible(nil); replacement != nil || err == nil {
		t.Fatalf("nil PTY master = %#v, %v", replacement, err)
	}
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if replacement, err := makePTYMasterInterruptible(file); replacement != nil || err == nil {
		t.Fatalf("closed PTY master = %#v, %v", replacement, err)
	}
}

func TestRuntimeMiscCloseoutCushionSpawnDescriptor(t *testing.T) {
	tool := NewSpawnTool(nil)
	if tool.Name() != "spawn" || tool.Description() == "" || len(tool.Parameters()) == 0 {
		t.Fatalf("spawn descriptor = %q/%q/%#v", tool.Name(), tool.Description(), tool.Parameters())
	}
}
