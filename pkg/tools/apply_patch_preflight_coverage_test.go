package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type applyPatchCoverageFileInfo struct {
	name string
	mode os.FileMode
	sys  any
}

func (info applyPatchCoverageFileInfo) Name() string       { return info.name }
func (info applyPatchCoverageFileInfo) Size() int64        { return 0 }
func (info applyPatchCoverageFileInfo) Mode() os.FileMode  { return info.mode }
func (info applyPatchCoverageFileInfo) ModTime() time.Time { return time.Time{} }
func (info applyPatchCoverageFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info applyPatchCoverageFileInfo) Sys() any           { return info.sys }

func TestApplyPatchCoverageParserAndExecuteErrors(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	workspace := t.TempDir()
	tool := NewApplyPatchTool(workspace, true)
	if result := tool.Execute(nil, map[string]any{
		"patch": "*** Begin Patch\n*** Add File: nil-context.txt\n+ok\n*** End Patch",
	}); result.IsError {
		t.Fatalf("nil-context execute = %#v", result)
	}
	for _, args := range []map[string]any{
		nil,
		{"patch": "   "},
		{"patch": "bad"},
		{"patch": "*** Begin Patch\n*** End Patch"},
	} {
		if result := tool.Execute(context.Background(), args); result == nil || !result.IsError {
			t.Fatalf("Execute(%#v) = %#v, want error", args, result)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseCodexPatchContext(
		ctx,
		"*** Begin Patch\n*** Add File: canceled.txt\n+x\n*** End Patch",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled parse error = %v", err)
	}

	parserFailures := []string{
		"*** Begin Patch\n*** Add File: a.txt\nnot-plus\n*** End Patch",
		"*** Begin Patch\n*** Add File: a.txt\n+x",
		"*** Begin Patch\n*** Delete File: a.txt\nunexpected\n*** End Patch",
		"*** Begin Patch\n*** Update File: a.txt\n*** End of File\n*** End Patch",
		"*** Begin Patch\n*** Update File: a.txt\n@@\n?bad\n*** End Patch",
		"*** Begin Patch\n*** Update File: a.txt\n@@\n-a\n*** End of File\n@@\n-b\n+c\n*** End Patch",
	}
	for _, patch := range parserFailures {
		if _, err := parseCodexPatchContext(context.Background(), patch); err == nil {
			t.Fatalf("parseCodexPatchContext(%q) error = nil", patch)
		}
	}
}

func TestApplyPatchCoverageParserCancellationAndBoundaries(t *testing.T) {
	largeLine := strings.Repeat("x", 128*1024)
	parserCases := []struct {
		name      string
		remaining int
		patch     string
	}{
		{
			name: "add loop cancellation", remaining: 2,
			patch: "*** Begin Patch\n*** Add File: a.txt\n+one\n+two\n*** End Patch",
		},
		{
			name: "add append cancellation", remaining: 3,
			patch: "*** Begin Patch\n*** Add File: a.txt\n+" + largeLine + "\n*** End Patch",
		},
		{
			name: "update loop cancellation", remaining: 2,
			patch: "*** Begin Patch\n*** Update File: a.txt\n@@\n-a\n+b\n*** End Patch",
		},
		{
			name: "hunk loop cancellation", remaining: 3,
			patch: "*** Begin Patch\n*** Update File: a.txt\n@@\n-a\n+b\n*** End Patch",
		},
	}
	for _, test := range parserCases {
		t.Run(test.name, func(t *testing.T) {
			ctx := &applyPatchCancelAfterChecksContext{
				Context: context.Background(), remaining: test.remaining,
			}
			if _, err := parseCodexPatchContext(ctx, test.patch); !errors.Is(err, context.Canceled) {
				t.Fatalf("parser cancellation error = %v", err)
			}
		})
	}

	for _, patch := range []string{
		"*** Begin Patch\n\n*** Add File: trailing.txt\n+x\n*** End Patch\n",
		"*** Begin Patch\n*** Update File: a.txt\n@@\n-a\n+b\n*** Delete File: b.txt\n*** End Patch",
	} {
		if _, err := parseCodexPatchContext(context.Background(), patch); err != nil {
			t.Fatalf("boundary parser case failed: %v", err)
		}
	}

	hunkCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 1,
	}
	if _, _, err := parseCodexPatchHunk(hunkCtx, []string{
		"*** Begin Patch", "-a", "+b", "*** End Patch",
	}, 1, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("direct hunk cancellation error = %v", err)
	}
	for _, lines := range [][]string{
		{"*** Begin Patch", "@@ next", "*** End Patch"},
		{"*** Begin Patch", applyPatchEndOfFileMarker, "*** End Patch"},
		{"*** Begin Patch", "-a", applyPatchEndOfFileMarker, "+later", "*** End Patch"},
	} {
		if _, _, err := parseCodexPatchHunk(context.Background(), lines, 1, ""); err == nil {
			t.Fatalf("direct hunk boundary %#v succeeded", lines)
		}
	}
	if _, _, err := parseCodexPatchHunk(context.Background(), []string{
		"*** Begin Patch", "", "-x", "*** End Patch",
	}, 1, ""); err != nil {
		t.Fatalf("blank hunk context failed: %v", err)
	}
}

func TestApplyPatchCoveragePrivateDefensiveBranches(t *testing.T) {
	if _, err := applyPatchLinkCount(nil, applyPatchCoverageFileInfo{}); err == nil {
		t.Fatal("nil link metadata succeeded")
	}
	if _, err := applyPatchLinkCount(nil, applyPatchCoverageFileInfo{sys: struct{ Other uint64 }{}}); err == nil {
		t.Fatal("missing Nlink metadata succeeded")
	}
	if _, err := applyPatchLinkCount(nil, applyPatchCoverageFileInfo{
		sys: struct{ Nlink string }{Nlink: "bad"},
	}); err == nil {
		t.Fatal("wrong-kind Nlink metadata succeeded")
	}
	if _, err := applyPatchLinkCount(nil, applyPatchCoverageFileInfo{
		sys: struct{ Nlink int64 }{Nlink: -1},
	}); err == nil {
		t.Fatal("negative Nlink metadata succeeded")
	}

	for _, workspace := range []string{"", " padded ", "invalid\x00workspace"} {
		if _, err := snapshotApplyPatchWorkspace(workspace); err == nil {
			t.Fatalf("snapshotApplyPatchWorkspace(%q) error = nil", workspace)
		}
	}
	fileWorkspace := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileWorkspace, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotApplyPatchWorkspace(fileWorkspace); err == nil {
		t.Fatal("file workspace succeeded")
	}
	if _, err := snapshotApplyPatchWorkspace(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing workspace succeeded")
	}
	if _, err := captureApplyPatchPathFences(filepath.Join(fileWorkspace, "child")); err == nil {
		t.Fatal("ENOTDIR fence capture succeeded")
	}

	if _, err := prepareApplyPatchProtectedRoots(t.TempDir(), []string{" "}); err == nil {
		t.Fatal("blank protected root succeeded")
	}
	if _, err := prepareApplyPatchProtectedRoots(t.TempDir(), []string{"invalid\x00root"}); err == nil {
		t.Fatal("NUL protected root succeeded")
	}
	prepared, prepareErr := prepareApplyPatchProtectedRoots("", []string{"relative-root"})
	if prepareErr != nil || len(prepared) != 1 || !filepath.IsAbs(prepared[0].lexical) {
		t.Fatalf("relative protected root = %#v, %v", prepared, prepareErr)
	}
	if _, err := prepareApplyPatchProtectedRoots(t.TempDir(), []string{
		filepath.Join(fileWorkspace, "child"),
	}); err == nil {
		t.Fatal("ENOTDIR protected root succeeded")
	}
	protectedWorkspace := t.TempDir()
	protectedRoot := filepath.Join(protectedWorkspace, "protected")
	if err := os.Mkdir(protectedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	driftPrepared, driftPrepareErr := prepareApplyPatchProtectedRoots(protectedWorkspace, []string{protectedRoot})
	if driftPrepareErr != nil {
		t.Fatal(driftPrepareErr)
	}
	if err := os.Rename(protectedRoot, protectedRoot+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(protectedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	protectedWorkspaceSnapshot, workspaceSnapshotErr := snapshotApplyPatchWorkspace(protectedWorkspace)
	if workspaceSnapshotErr != nil {
		t.Fatal(workspaceSnapshotErr)
	}
	if _, err := snapshotApplyPatchProtectedRoots(protectedWorkspaceSnapshot, driftPrepared); err == nil {
		t.Fatal("changed construction protected root succeeded")
	}

	gateWorkspace := t.TempDir()
	var invalidCoordinator applyPatchGateCoordinator
	if _, _, err := invalidCoordinator.lock(context.Background(), ""); err == nil {
		t.Fatal("invalid workspace gate lock succeeded")
	}
	_, unlockGate, lockErr := globalApplyPatchGates.lock(context.Background(), gateWorkspace)
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	defer unlockGate()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := globalApplyPatchGates.lock(ctx, gateWorkspace); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled gate lock error = %v", err)
	}

	if _, err := replaceApplyPatchMatchContext(
		context.Background(), []byte("abc"), -1, 1, []byte("x"),
	); err == nil {
		t.Fatal("negative replacement index succeeded")
	}
	if index, count, err := findUniqueApplyPatchMatch(
		context.Background(), []byte("abc"), nil,
	); err != nil || index != 0 || count != 0 {
		t.Fatalf("empty needle = (%d,%d,%v)", index, count, err)
	}
	kmpCtx, kmpCancel := context.WithCancel(context.Background())
	kmpCancel()
	if _, _, err := findUniqueApplyPatchMatch(
		kmpCtx, nil, []byte(strings.Repeat("a", 64*1024+1)),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("KMP prefix cancellation error = %v", err)
	}
	if equal, err := equalApplyPatchBytesContext(
		context.Background(), []byte("a"), []byte("bb"),
	); err != nil || equal {
		t.Fatalf("different-length equality = %t, %v", equal, err)
	}
	if equal, err := equalApplyPatchBytesContext(
		context.Background(), []byte("a"), []byte("b"),
	); err != nil || equal {
		t.Fatalf("different-content equality = %t, %v", equal, err)
	}
	if !applyPatchPathWithinExact("same", "same") {
		t.Fatal("equal exact paths were not contained")
	}
}

func TestApplyPatchCoverageChunkedErrorPropagation(t *testing.T) {
	largeLine := strings.Repeat("x", 128*1024)
	hunkCases := []struct {
		name      string
		remaining int
		hunk      codexPatchHunk
	}{
		{
			name: "context old copy", remaining: 2,
			hunk: codexPatchHunk{lines: []codexPatchLine{{
				kind: ' ', text: largeLine, newline: true,
			}}},
		},
		{
			name: "context new copy", remaining: 4,
			hunk: codexPatchHunk{lines: []codexPatchLine{{
				kind: ' ', text: "x", newline: true,
			}}},
		},
		{
			name: "deletion copy", remaining: 2,
			hunk: codexPatchHunk{lines: []codexPatchLine{{
				kind: '-', text: largeLine, newline: true,
			}}},
		},
		{
			name: "addition copy", remaining: 2,
			hunk: codexPatchHunk{lines: []codexPatchLine{{
				kind: '+', text: largeLine, newline: true,
			}}},
		},
	}
	for _, test := range hunkCases {
		t.Run(test.name, func(t *testing.T) {
			ctx := &applyPatchCancelAfterChecksContext{
				Context: context.Background(), remaining: test.remaining,
			}
			if _, _, err := codexPatchHunkBytesContext(ctx, test.hunk); !errors.Is(err, context.Canceled) {
				t.Fatalf("hunk materialization error = %v", err)
			}
		})
	}

	validationCtx, validationCancel := context.WithCancel(context.Background())
	validationCancel()
	if err := validateCodexPatchHunk(validationCtx, codexPatchHunk{
		lines: []codexPatchLine{{kind: '-', text: "x", newline: true}},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("hunk validation cancellation error = %v", err)
	}

	applyCases := []struct {
		name      string
		remaining int
		before    []byte
		hunk      codexPatchHunk
	}{
		{name: "initial copy", remaining: 1, before: []byte("x")},
		{
			name: "hunk loop", remaining: 2,
			hunk: codexPatchHunk{lines: []codexPatchLine{{kind: '-', text: "", newline: true}}},
		},
		{
			name: "hunk materialization", remaining: 3,
			hunk: codexPatchHunk{lines: []codexPatchLine{{kind: '-', text: "", newline: true}}},
		},
		{
			name: "end insertion copy", remaining: 5,
			hunk: codexPatchHunk{
				lines: []codexPatchLine{{kind: '+', text: "", newline: true}}, endOfFile: true,
			},
		},
		{
			name: "end match", remaining: 6, before: []byte("\n"),
			hunk: codexPatchHunk{
				lines: []codexPatchLine{{kind: '-', text: "", newline: true}}, endOfFile: true,
			},
		},
		{
			name: "regular match", remaining: 6, before: []byte("\n"),
			hunk: codexPatchHunk{lines: []codexPatchLine{{kind: '-', text: "", newline: true}}},
		},
		{
			name: "end replacement", remaining: 9, before: []byte("x"),
			hunk: codexPatchHunk{
				lines: []codexPatchLine{{kind: '-', text: "x"}}, endOfFile: true,
			},
		},
		{
			name: "regular replacement", remaining: 13, before: []byte("x"),
			hunk: codexPatchHunk{lines: []codexPatchLine{
				{kind: '-', text: "x"}, {kind: '+', text: "y"},
			}},
		},
	}
	for _, test := range applyCases {
		t.Run("apply "+test.name, func(t *testing.T) {
			ctx := &applyPatchCancelAfterChecksContext{
				Context: context.Background(), remaining: test.remaining,
			}
			_, applyErr := applyCodexPatchHunks(
				ctx, test.before, "source", []codexPatchHunk{test.hunk},
			)
			if !errors.Is(applyErr, context.Canceled) {
				t.Fatalf("apply hunk cancellation error = %v", applyErr)
			}
		})
	}
	if _, err := applyCodexPatchHunks(context.Background(), []byte("x-x"), "source", []codexPatchHunk{{
		lines: []codexPatchLine{{kind: '-', text: "x"}}, endOfFile: true,
	}}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous end-of-file hunk error = %v", err)
	}

	prefixCtx, prefixCancel := context.WithCancel(context.Background())
	prefixCancel()
	if _, err := replaceApplyPatchMatchContext(
		prefixCtx, []byte("abc"), 1, 1, nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("replacement prefix cancellation error = %v", err)
	}
	replacementCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 2,
	}
	if _, err := replaceApplyPatchMatchContext(
		replacementCtx, []byte("a"), 0, 0, []byte("x"),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("replacement content cancellation error = %v", err)
	}
}

func TestApplyPatchCoveragePlanningAndCommitFailures(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	workspace := t.TempDir()
	workspaceSnapshot, workspaceErr := snapshotApplyPatchWorkspace(workspace)
	if workspaceErr != nil {
		t.Fatal(workspaceErr)
	}
	tool := NewApplyPatchTool(workspace, true)
	if _, err := tool.resolveWritePath(" "); err == nil {
		t.Fatal("blank resolveWritePath succeeded")
	}
	guarded := NewApplyPatchToolWithPathGuard(workspace, true, func(string) error {
		return errors.New("denied")
	})
	if err := guarded.guardApplyPatchPath(context.Background(), "move.txt", true); err == nil {
		t.Fatal("move guard denial succeeded")
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tool.guardApplyPatchPath(canceledCtx, "canceled.txt", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled path guard error = %v", err)
	}
	if _, planErr := tool.planPatch(canceledCtx, workspaceSnapshot, []codexPatchOp{{
		kind: "add", path: "canceled.txt", add: []byte("x"),
	}}); !errors.Is(planErr, context.Canceled) {
		t.Fatalf("canceled operation planning error = %v", planErr)
	}
	protected, protectedErr := prepareApplyPatchProtectedRoots(workspace, []string{
		filepath.Join(workspace, "protected"),
	})
	if protectedErr != nil {
		t.Fatal(protectedErr)
	}
	protectedTool := &ApplyPatchTool{protectedRoots: protected}
	_, protectedPlanErr := protectedTool.planPatch(canceledCtx, workspaceSnapshot, nil)
	if !errors.Is(protectedPlanErr, context.Canceled) {
		t.Fatalf("canceled protected-root planning error = %v", protectedPlanErr)
	}
	if _, planErr := tool.planPatch(context.Background(), workspaceSnapshot, []codexPatchOp{{
		kind: "unsupported", path: "missing.txt",
	}}); planErr == nil {
		t.Fatal("unsupported operation planned")
	}
	if _, candidateErr := tool.resolveApplyPatchCandidate(
		&applyPatchPlan{workspace: workspaceSnapshot}, "invalid\x00path",
	); candidateErr == nil {
		t.Fatal("invalid candidate path succeeded")
	}
	blockedParent := filepath.Join(workspace, "blocked-parent")
	if err := os.WriteFile(blockedParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, candidateErr := tool.resolveApplyPatchCandidate(
		&applyPatchPlan{workspace: workspaceSnapshot}, filepath.Join("blocked-parent", "child"),
	); candidateErr == nil {
		t.Fatal("ENOTDIR candidate succeeded")
	}
	outside := filepath.Join(filepath.Dir(workspace), "outside-"+filepath.Base(workspace))
	if err := tool.authorizeApplyPatchCanonical(
		&applyPatchPlan{workspace: workspaceSnapshot}, outside,
	); err == nil {
		t.Fatal("outside candidate authorization succeeded")
	}
	driftPath := filepath.Join(workspace, "drift.txt")
	if err := os.WriteFile(driftPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	driftTool := NewApplyPatchTool(workspace, true)
	driftTool.beforePathFence = func(string) {
		if err := os.Rename(driftPath, driftPath+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(driftPath, []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, candidateErr := driftTool.resolveApplyPatchCandidate(
		&applyPatchPlan{workspace: workspaceSnapshot}, "drift.txt",
	); candidateErr == nil {
		t.Fatal("changed existing candidate succeeded")
	}
	appearedPath := filepath.Join(workspace, "appeared.txt")
	appearedTool := NewApplyPatchTool(workspace, true)
	appearedTool.beforePathFence = func(string) {
		if err := os.WriteFile(appearedPath, []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, candidateErr := appearedTool.resolveApplyPatchCandidate(
		&applyPatchPlan{workspace: workspaceSnapshot}, "appeared.txt",
	); candidateErr == nil {
		t.Fatal("appeared candidate succeeded")
	}
	reauthorizePlan := &applyPatchPlan{workspace: workspaceSnapshot}
	reauthorizeTool := NewApplyPatchTool(workspace, true)
	reauthorizeTool.beforePathFence = func(string) {
		reauthorizePlan.protectedRoots = []applyPatchProtectedRoot{{
			canonical: workspaceSnapshot.canonical,
		}}
	}
	if _, candidateErr := reauthorizeTool.resolveApplyPatchCandidate(
		reauthorizePlan, "reauthorized.txt",
	); candidateErr == nil {
		t.Fatal("second canonical authorization was not enforced")
	}
	if _, sourceErr := snapshotApplyPatchSource(
		context.Background(), applyPatchCandidate{}, "missing", nil,
	); sourceErr == nil {
		t.Fatal("missing source snapshot succeeded")
	}
	if _, sourceErr := snapshotApplyPatchSource(context.Background(), applyPatchCandidate{
		canonical: filepath.Join(workspace, "missing-handle"),
		exists:    true,
		info:      applyPatchCoverageFileInfo{name: "missing-handle", mode: 0o600},
	}, "missing-handle", nil); sourceErr == nil {
		t.Fatal("missing source handle snapshot succeeded")
	}
	directory := filepath.Join(workspace, "directory")
	if mkdirErr := os.Mkdir(directory, 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	directoryInfo, directoryInfoErr := os.Lstat(directory)
	if directoryInfoErr != nil {
		t.Fatal(directoryInfoErr)
	}
	if _, err := snapshotApplyPatchSource(context.Background(), applyPatchCandidate{
		canonical: directory, exists: true, info: directoryInfo,
	}, "directory", nil); err == nil {
		t.Fatal("directory source snapshot succeeded")
	}
	directoryHandle, directoryOpenErr := os.Open(directory)
	if directoryOpenErr != nil {
		t.Fatal(directoryOpenErr)
	}
	if _, err := readApplyPatchSourceContext(context.Background(), directoryHandle); err == nil {
		_ = directoryHandle.Close()
		t.Fatal("directory source read succeeded")
	}
	if err := directoryHandle.Close(); err != nil {
		t.Fatal(err)
	}
	cancelSourcePath := filepath.Join(workspace, "cancel-source.txt")
	if err := os.WriteFile(cancelSourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	cancelSourceInfo, cancelSourceInfoErr := os.Lstat(cancelSourcePath)
	if cancelSourceInfoErr != nil {
		t.Fatal(cancelSourceInfoErr)
	}
	sourceCtx, sourceCancel := context.WithCancel(context.Background())
	if _, err := snapshotApplyPatchSource(sourceCtx, applyPatchCandidate{
		canonical: cancelSourcePath, exists: true, info: cancelSourceInfo,
	}, "cancel-source.txt", func(string) { sourceCancel() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-open source cancellation error = %v", err)
	}
	readCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 2,
	}
	if _, err := snapshotApplyPatchSource(readCtx, applyPatchCandidate{
		canonical: cancelSourcePath, exists: true, info: cancelSourceInfo,
	}, "cancel-source.txt", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("source read cancellation error = %v", err)
	}
	emptyPlanSource := filepath.Join(workspace, "empty-plan-source.txt")
	if err := os.WriteFile(emptyPlanSource, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	planCancellationCases := []struct {
		name      string
		remaining int
		op        codexPatchOp
	}{
		{
			name: "add content copy", remaining: 2,
			op: codexPatchOp{
				kind: "add", path: "canceled-add.txt", add: []byte(strings.Repeat("a", 128*1024)),
			},
		},
		{
			name: "delete source copy", remaining: 4,
			op: codexPatchOp{kind: "delete", path: "empty-plan-source.txt"},
		},
		{
			name: "update source copy", remaining: 5,
			op: codexPatchOp{kind: "update", path: "empty-plan-source.txt"},
		},
	}
	for _, test := range planCancellationCases {
		t.Run(test.name, func(t *testing.T) {
			ctx := &applyPatchCancelAfterChecksContext{
				Context: context.Background(), remaining: test.remaining,
			}
			if _, _, err := tool.planPatchOperation(
				ctx, &applyPatchPlan{workspace: workspaceSnapshot}, test.op,
			); !errors.Is(err, context.Canceled) {
				t.Fatalf("operation cancellation error = %v", err)
			}
		})
	}
	moveGuardTool := NewApplyPatchToolWithPathGuard(workspace, true, func(path string) error {
		if path == "move-target.txt" {
			return errors.New("move denied")
		}
		return nil
	})
	if _, _, err := moveGuardTool.planPatchOperation(
		context.Background(), &applyPatchPlan{workspace: workspaceSnapshot}, codexPatchOp{
			kind: "update", path: "empty-plan-source.txt", moveTo: "move-target.txt",
		},
	); err == nil {
		t.Fatal("denied move operation planned")
	}

	regularParent := filepath.Join(workspace, "parent-file")
	if err := os.WriteFile(regularParent, []byte("parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitCases := []struct {
		name string
		op   plannedApplyPatchOp
	}{
		{
			name: "add mkdir",
			op: plannedApplyPatchOp{kind: "add", targetLabel: "child", targetPath: filepath.Join(
				regularParent, "child",
			), after: []byte("x")},
		},
		{
			name: "add write directory",
			op: plannedApplyPatchOp{
				kind: "add", targetLabel: "directory", targetPath: directory,
				after: []byte("x"),
			},
		},
		{
			name: "delete missing",
			op: plannedApplyPatchOp{kind: "delete", sourceLabel: "missing", sourcePath: filepath.Join(
				workspace, "missing",
			)},
		},
		{
			name: "update directory",
			op: plannedApplyPatchOp{
				kind: "update", targetLabel: "directory", targetPath: directory,
				after: []byte("x"),
			},
		},
		{
			name: "move mkdir",
			op: plannedApplyPatchOp{
				kind: "move", sourceLabel: "source", targetLabel: "target",
				sourcePath: filepath.Join(workspace, "missing-source"),
				targetPath: filepath.Join(regularParent, "target"), after: []byte("x"),
			},
		},
		{
			name: "move write directory",
			op: plannedApplyPatchOp{
				kind: "move", sourceLabel: "source", targetLabel: "directory",
				sourcePath: filepath.Join(workspace, "missing-source"), targetPath: directory,
				after: []byte("x"),
			},
		},
	}
	for _, test := range commitCases {
		t.Run(test.name, func(t *testing.T) {
			if err := commitApplyPatchPlan(&applyPatchPlan{ops: []plannedApplyPatchOp{test.op}}); err == nil {
				t.Fatal("commit failure case returned nil")
			}
		})
	}

	source := filepath.Join(workspace, "remove-failure-source")
	target := filepath.Join(workspace, "remove-failure-target")
	if err := commitApplyPatchPlan(&applyPatchPlan{ops: []plannedApplyPatchOp{{
		kind: "move", sourceLabel: "source", targetLabel: "target",
		sourcePath: source, targetPath: target, after: []byte("written"),
	}}}); err == nil {
		t.Fatal("move remove failure returned nil")
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "written" {
		t.Fatalf("move failure target = %q, %v", content, err)
	}

	executeFailureTarget := filepath.Join(workspace, "execute-failure")
	executeFailureTool := NewApplyPatchTool(workspace, true)
	executeFailureTool.afterPointOfNoReturn = func(*applyPatchPlan) {
		if err := os.Mkdir(executeFailureTarget, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	result := executeFailureTool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Add File: execute-failure\n+x\n*** End Patch",
	})
	if result == nil || !result.IsError {
		t.Fatalf("execute commit failure = %#v", result)
	}
}

func TestApplyPatchCoverageRevalidationFailures(t *testing.T) {
	workspace := t.TempDir()
	workspaceSnapshot, workspaceSnapshotErr := snapshotApplyPatchWorkspace(workspace)
	if workspaceSnapshotErr != nil {
		t.Fatal(workspaceSnapshotErr)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := revalidateApplyPatchPlan(canceledCtx, &applyPatchPlan{
		workspace: workspaceSnapshot,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("initial revalidation cancellation error = %v", err)
	}
	fenceCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 2,
	}
	if err := revalidateApplyPatchPlan(fenceCtx, &applyPatchPlan{
		workspace: workspaceSnapshot, fences: workspaceSnapshot.fences,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("fence revalidation cancellation error = %v", err)
	}
	opCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 2,
	}
	if err := revalidateApplyPatchPlan(opCtx, &applyPatchPlan{
		workspace: workspaceSnapshot, ops: []plannedApplyPatchOp{{kind: "add"}},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("operation revalidation cancellation error = %v", err)
	}

	removedParent := t.TempDir()
	removedWorkspace := filepath.Join(removedParent, "workspace")
	if err := os.Mkdir(removedWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	removedSnapshot, removedSnapshotErr := snapshotApplyPatchWorkspace(removedWorkspace)
	if removedSnapshotErr != nil {
		t.Fatal(removedSnapshotErr)
	}
	if err := os.Remove(removedWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchPlan(context.Background(), &applyPatchPlan{
		workspace: removedSnapshot,
	}); err == nil {
		t.Fatal("removed workspace revalidation succeeded")
	}

	root := filepath.Join(workspace, "protected")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	prepared, prepareErr := prepareApplyPatchProtectedRoots(workspace, []string{root})
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	protectedSnapshots, protectedSnapshotsErr := snapshotApplyPatchProtectedRoots(workspaceSnapshot, prepared)
	if protectedSnapshotsErr != nil {
		t.Fatal(protectedSnapshotsErr)
	}
	otherRoot := filepath.Join(workspace, "other-protected")
	if err := os.Mkdir(otherRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(otherRoot, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := revalidateApplyPatchPlan(context.Background(), &applyPatchPlan{
		workspace: workspaceSnapshot, protectedRoots: protectedSnapshots,
	}); err == nil {
		t.Fatal("changed protected root revalidation succeeded")
	}

	missingInfo := applyPatchCoverageFileInfo{name: "missing", mode: 0o600}
	if err := revalidateApplyPatchPlan(context.Background(), &applyPatchPlan{
		workspace: workspaceSnapshot,
		ops: []plannedApplyPatchOp{{
			sourceLabel: "missing",
			source: &applyPatchFileSnapshot{
				path: filepath.Join(workspace, "missing"), info: missingInfo,
			},
		}},
	}); err == nil {
		t.Fatal("missing source revalidation succeeded")
	}
	emptySourcePath := filepath.Join(workspace, "empty-source.txt")
	if err := os.WriteFile(emptySourcePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	emptySourceInfo, emptySourceInfoErr := os.Lstat(emptySourcePath)
	if emptySourceInfoErr != nil {
		t.Fatal(emptySourceInfoErr)
	}
	emptySourceSnapshot, emptySourceSnapshotErr := snapshotApplyPatchSource(context.Background(), applyPatchCandidate{
		canonical: emptySourcePath, exists: true, info: emptySourceInfo,
	}, "empty-source.txt", nil)
	if emptySourceSnapshotErr != nil {
		t.Fatal(emptySourceSnapshotErr)
	}
	equalityCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 5,
	}
	if err := revalidateApplyPatchPlan(equalityCtx, &applyPatchPlan{
		workspace: workspaceSnapshot,
		ops: []plannedApplyPatchOp{{
			sourceLabel: "empty-source.txt", source: emptySourceSnapshot,
		}},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("source equality cancellation error = %v", err)
	}

	sourcePath := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceInfo, sourceInfoErr := os.Lstat(sourcePath)
	if sourceInfoErr != nil {
		t.Fatal(sourceInfoErr)
	}
	sourceSnapshot, sourceSnapshotErr := snapshotApplyPatchSource(context.Background(), applyPatchCandidate{
		canonical: sourcePath, exists: true, info: sourceInfo,
	}, "source.txt", nil)
	if sourceSnapshotErr != nil {
		t.Fatal(sourceSnapshotErr)
	}
	sourceSnapshot.data = []byte("planned-different-data")
	if err := revalidateApplyPatchPlan(context.Background(), &applyPatchPlan{
		workspace: workspaceSnapshot,
		ops:       []plannedApplyPatchOp{{sourceLabel: "source.txt", source: sourceSnapshot}},
	}); err == nil {
		t.Fatal("changed source data revalidation succeeded")
	}
}

func TestApplyPatchCoverageFenceAndResolutionFailures(t *testing.T) {
	root := t.TempDir()
	absent := filepath.Join(root, "absent")
	fences, fenceErr := captureApplyPatchPathFences(absent)
	if fenceErr != nil || len(fences) == 0 || fences[0].exists {
		t.Fatalf("absent fences = %#v, %v", fences, fenceErr)
	}
	if writeErr := os.WriteFile(absent, []byte("appeared"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if revalidateErr := revalidateApplyPatchFence(fences[0]); revalidateErr == nil {
		t.Fatal("appeared absent fence succeeded")
	}

	existingInfo, existingInfoErr := os.Lstat(absent)
	if existingInfoErr != nil {
		t.Fatal(existingInfoErr)
	}
	existingFence := applyPatchPathFence{
		path: absent, exists: true, info: existingInfo, mode: existingInfo.Mode(),
	}
	if err := os.Chmod(absent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchFence(existingFence); err == nil {
		t.Fatal("mode-drift fence succeeded")
	}

	regularParent := filepath.Join(root, "regular")
	if err := os.WriteFile(regularParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveApplyPatchPathAgainstExistingAncestor(
		filepath.Join(regularParent, "child"),
	); err == nil {
		t.Fatal("ENOTDIR ancestor resolution succeeded")
	}
	if _, err := statApplyPatchExistingAncestor(filepath.Join(regularParent, "child")); err == nil {
		t.Fatal("ENOTDIR ancestor stat succeeded")
	}
	if err := validateApplyPatchAncestorChain(regularParent); err == nil {
		t.Fatal("regular-file ancestor chain succeeded")
	}
	if err := validateApplyPatchAncestorChain(filepath.Join(regularParent, "child")); err == nil {
		t.Fatal("ENOTDIR ancestor chain succeeded")
	}
	regularLink := filepath.Join(root, "regular-link")
	if err := os.Symlink(regularParent, regularLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := validateApplyPatchAncestorChain(regularLink); err == nil {
		t.Fatal("regular-file symlink ancestor succeeded")
	}
	if applyPatchExistingAncestorContains(root, filepath.Join(regularParent, "child")) {
		t.Fatal("ENOTDIR candidate was contained by existing ancestor")
	}

	if got := fmt.Sprint(dedupeApplyPatchFences([]applyPatchPathFence{
		{path: absent}, {path: absent},
	})); got == "[]" {
		t.Fatal("dedupe removed every fence")
	}
}
