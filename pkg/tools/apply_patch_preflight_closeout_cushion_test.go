package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type applyPatchCloseoutMutationContext struct {
	context.Context
	calls  int
	mutate func()
}

func (ctx *applyPatchCloseoutMutationContext) Err() error {
	ctx.calls++
	if ctx.calls == 2 && ctx.mutate != nil {
		ctx.mutate()
	}
	return nil
}

func TestApplyPatchPreflightCloseoutCushionWorkspaceAndRoots(t *testing.T) {
	for _, workspace := range []string{"", " padded ", "bad\x00path", filepath.Join(t.TempDir(), "missing")} {
		if snapshot, err := snapshotApplyPatchWorkspace(workspace); snapshot.canonical != "" || err == nil {
			t.Fatalf("invalid workspace %q = %#v, %v", workspace, snapshot, err)
		}
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotApplyPatchWorkspace(file); err == nil {
		t.Fatal("file workspace was accepted")
	}
	if roots, err := prepareApplyPatchProtectedRoots("", nil); roots != nil || err != nil {
		t.Fatalf("empty protected roots = %#v, %v", roots, err)
	}
	for _, root := range []string{"", " padded ", "bad\x00root"} {
		if _, err := prepareApplyPatchProtectedRoots(t.TempDir(), []string{root}); err == nil {
			t.Fatalf("invalid protected root %q accepted", root)
		}
	}
}

func TestApplyPatchPreflightCloseoutCushionGuardAndCandidateErrors(t *testing.T) {
	workspace := t.TempDir()
	tool := NewApplyPatchTool(workspace, true)
	snapshot, err := snapshotApplyPatchWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	plan := &applyPatchPlan{workspace: snapshot}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tool.guardApplyPatchPath(canceled, "file", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled path guard = %v", err)
	}
	if err := tool.guardApplyPatchPath(context.Background(), ".git/config", false); err == nil {
		t.Fatal("Git path guard succeeded")
	}
	tool.pathGuard = func(string) error { return errors.New("denied") }
	if err := tool.guardApplyPatchPath(context.Background(), "file", false); err == nil {
		t.Fatal("source path guard denial succeeded")
	}
	if err := tool.guardApplyPatchPath(context.Background(), "file", true); err == nil {
		t.Fatal("move path guard denial succeeded")
	}
	for _, label := range []string{"", "bad\x00path"} {
		if _, err := tool.resolveApplyPatchCandidate(plan, label); err == nil {
			t.Fatalf("invalid candidate %q resolved", label)
		}
	}
	symlink := filepath.Join(workspace, "link")
	if err := os.Symlink("missing", symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.resolveApplyPatchCandidate(plan, "link"); err == nil {
		t.Fatal("terminal symlink candidate resolved")
	}
	parentFile := filepath.Join(workspace, "parent")
	if err := os.WriteFile(parentFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.resolveApplyPatchCandidate(plan, "parent/child"); err == nil {
		t.Fatal("file-ancestor candidate resolved")
	}
}

func TestApplyPatchPreflightCloseoutCushionSourceAndFenceErrors(t *testing.T) {
	if _, err := snapshotApplyPatchSource(
		context.Background(), applyPatchCandidate{}, "missing", nil,
	); err == nil {
		t.Fatal("absent source snapshotted")
	}
	directory := t.TempDir()
	info, infoErr := os.Lstat(directory)
	if infoErr != nil {
		t.Fatal(infoErr)
	}
	if _, err := snapshotApplyPatchSource(
		context.Background(),
		applyPatchCandidate{exists: true, info: info, canonical: directory},
		"directory",
		nil,
	); err == nil {
		t.Fatal("directory source snapshotted")
	}
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, infoErr = os.Lstat(path)
	if infoErr != nil {
		t.Fatal(infoErr)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshotApplyPatchSource(
		canceled,
		applyPatchCandidate{exists: true, info: info, canonical: path},
		"source",
		func(string) {},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source snapshot = %v", err)
	}
	if _, err := snapshotApplyPatchSource(
		context.Background(),
		applyPatchCandidate{exists: true, info: info, canonical: path},
		"source",
		func(string) { _ = os.Remove(path) },
	); err == nil {
		t.Fatal("removed source snapshotted")
	}

	linkRoot := t.TempDir()
	targetA := filepath.Join(linkRoot, "a")
	targetB := filepath.Join(linkRoot, "b")
	if err := os.Mkdir(targetA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(targetB, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkRoot, "link")
	if err := os.Symlink(targetA, link); err != nil {
		t.Fatal(err)
	}
	fences, fenceErr := captureApplyPatchPathFences(link)
	if fenceErr != nil || len(fences) == 0 {
		t.Fatal(fenceErr)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetB, link); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchFence(fences[0]); err == nil {
		t.Fatal("changed symlink fence revalidated")
	}
}

func TestApplyPatchPreflightCloseoutCushionPathHelpers(t *testing.T) {
	if err := validateApplyPatchAncestorChain(filepath.Join(t.TempDir(), "missing", "child")); err != nil {
		t.Fatalf("missing ancestor chain = %v", err)
	}
	missingPath := filepath.Join(string(os.PathSeparator), "missing", "child")
	if _, err := statApplyPatchExistingAncestor(missingPath); err != nil {
		t.Fatalf("root ancestor lookup = %v", err)
	}
	if !applyPatchPathWithinExact("/a/b", "/a") || applyPatchPathWithinExact("/ab", "/a") {
		t.Fatal("exact path containment mismatch")
	}
	fences := dedupeApplyPatchFences([]applyPatchPathFence{
		{path: "/a"}, {path: "/a/../a"}, {path: "/b"},
	})
	if len(fences) != 2 {
		t.Fatalf("deduped fences = %#v", fences)
	}
}

func TestApplyPatchPreflightCloseoutCushionSourceReadDrift(t *testing.T) {
	for _, drift := range []string{"mode", "links"} {
		t.Run(drift, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "source")
			if err := os.WriteFile(path, make([]byte, 128<<10), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx := &applyPatchCloseoutMutationContext{Context: context.Background()}
			ctx.mutate = func() {
				if drift == "mode" {
					_ = os.Chmod(path, 0o640)
				} else {
					_ = os.Link(path, filepath.Join(directory, "extra-link"))
				}
			}
			if _, err := snapshotApplyPatchSource(
				ctx,
				applyPatchCandidate{exists: true, info: info, canonical: path},
				"source",
				nil,
			); err == nil {
				t.Fatalf("%s drifted source was accepted", drift)
			}
		})
	}
}

func TestApplyPatchPreflightCloseoutCushionLateParentAndCommitReturn(t *testing.T) {
	workspace := t.TempDir()
	tool := NewApplyPatchTool(workspace, true)
	snapshot, err := snapshotApplyPatchWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	tool.beforePathFence = func(string) {
		_ = os.WriteFile(filepath.Join(workspace, "late-parent"), []byte("file"), 0o600)
	}
	if _, err := tool.resolveApplyPatchCandidate(
		&applyPatchPlan{workspace: snapshot},
		"late-parent/child",
	); err == nil {
		t.Fatal("late file parent was accepted")
	}
	if err := commitApplyPatchPlan(&applyPatchPlan{}); err != nil {
		t.Fatalf("empty commit plan = %v", err)
	}
}
