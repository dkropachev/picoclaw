//go:build windows

package tools

import (
	"context"
	"testing"
)

func TestApplyPatchPreflightRejectsWindowsCaseAliasTargets(t *testing.T) {
	workspace := t.TempDir()
	before := applyPatchSnapshotTree(t, workspace)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Add File: Case.txt\n+first\n"+
			"*** Add File: case.TXT\n+second\n"+
			"*** End Patch",
	)
	requireApplyPatchError(t, result)
	assertApplyPatchTreeEqual(t, workspace, before)
}

func TestApplyPatchPathKeyUsesWindowsCaseInsensitiveIdentity(t *testing.T) {
	first := applyPatchPathKey(`C:\Workspace\Case.txt`)
	second := applyPatchPathKey(`c:\workspace\case.TXT`)
	if first != second {
		t.Fatalf("Windows path keys differ: %q != %q", first, second)
	}
}

func TestApplyPatchWindowsPathPolicyRejectsAliasesAndStreams(t *testing.T) {
	for _, path := range []string{
		`parent.`, `parent `, `parent.\child.txt`, `base.txt::$DATA`, `NUL.txt`,
		`SESSION~1\private.jsonl`,
	} {
		if err := validateApplyPatchPlatformPath(path); err == nil {
			t.Fatalf("validateApplyPatchPlatformPath(%q) error = nil", path)
		}
	}
	if first, second := applyPatchPathKey(`C:\workspace\parent.`),
		applyPatchPathKey(`c:\WORKSPACE\parent`); first != second {
		t.Fatalf("Windows trailing-dot aliases differ: %q != %q", first, second)
	}
}

func TestApplyPatchPathWithinUsesWindowsFilesystemIdentity(t *testing.T) {
	if !applyPatchPathWithinIdentity(`C:\WORKSPACE\Sessions\private.jsonl`, `c:\workspace\sessions`) {
		t.Fatal("Windows case alias bypassed protected-root containment")
	}
}

func TestApplyPatchExactContainmentDoesNotGrantWindowsFoldedSibling(t *testing.T) {
	if applyPatchPathWithinExact(`C:\foo\Work\secret`, `C:\foo\work`) {
		t.Fatal("Windows folded sibling was treated as exact workspace containment")
	}
}
