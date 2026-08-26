//go:build darwin

package tools

import "testing"

func TestApplyPatchPathKeyUsesDarwinCaseAndNormalizationIdentity(t *testing.T) {
	first := applyPatchPathKey("/workspace/É.txt")
	second := applyPatchPathKey("/workspace/e\u0301.TXT")
	if first != second {
		t.Fatalf("Darwin path keys differ: %q != %q", first, second)
	}
}

func TestApplyPatchPathWithinUsesDarwinFilesystemIdentity(t *testing.T) {
	root := "/workspace/Séssions"
	candidate := "/WORKSPACE/se\u0301ssions/private.jsonl"
	if !applyPatchPathWithinIdentity(candidate, root) {
		t.Fatalf("Darwin alias %q was not within protected root %q", candidate, root)
	}
}

func TestApplyPatchExactContainmentDoesNotGrantDarwinFoldedSibling(t *testing.T) {
	if applyPatchPathWithinExact("/foo/Work/secret", "/foo/work") {
		t.Fatal("Darwin folded sibling was treated as exact workspace containment")
	}
}

func TestApplyPatchPathKeyUsesDarwinUnicodeCaseFolding(t *testing.T) {
	first := applyPatchPathKey("/workspace/sessions/private.jsonl")
	second := applyPatchPathKey("/WORKSPACE/ſeſſionſ/private.jsonl")
	if first != second {
		t.Fatalf("Darwin Unicode-folded keys differ: %q != %q", first, second)
	}
}
