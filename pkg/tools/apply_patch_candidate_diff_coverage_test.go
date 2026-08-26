package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestApplyPatchCandidateDiffDefensiveCoverage(t *testing.T) {
	if _, err := buildApplyPatchCandidateResult(context.Background(), nil); err == nil {
		t.Fatal("nil candidate plan succeeded")
	}
	valid := &applyPatchPlan{
		ops:       []plannedApplyPatchOp{{kind: "add", targetLabel: "valid.txt"}},
		summaries: []string{"added valid.txt"},
	}
	if result, err := buildApplyPatchCandidateResult(nil, valid); err != nil || result == nil {
		t.Fatalf("nil-context candidate = %#v, %v", result, err)
	}
	tooMany := &applyPatchPlan{ops: make([]plannedApplyPatchOp, applyPatchCandidateMaxOperations+1)}
	if _, err := buildApplyPatchCandidateResult(context.Background(), tooMany); err == nil {
		t.Fatal("over-budget candidate succeeded")
	}
	unsupported := &applyPatchPlan{ops: []plannedApplyPatchOp{{
		kind: "unsupported", sourceLabel: "source", targetLabel: "target",
	}}}
	if _, err := buildApplyPatchCandidateResult(context.Background(), unsupported); err == nil {
		t.Fatal("unsupported candidate operation succeeded")
	}
	if labels := applyPatchCandidateLogicalLabels(plannedApplyPatchOp{kind: "unsupported"}); labels != nil {
		t.Fatalf("unsupported logical labels = %#v", labels)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateApplyPatchCandidateBudgets(canceled, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled budget error = %v", err)
	}
	invalidLabel := &applyPatchPlan{ops: []plannedApplyPatchOp{{
		kind: "add", targetLabel: "bad\x00path",
	}}}
	if err := validateApplyPatchCandidateBudgets(context.Background(), invalidLabel); err == nil {
		t.Fatal("invalid budget path succeeded")
	}
	if _, err := countApplyPatchCandidateLines(canceled, []byte("line\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled line count error = %v", err)
	}

	loopCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 5,
	}
	if _, err := buildApplyPatchCandidateResult(loopCtx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("candidate loop cancellation error = %v", err)
	}
	countCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 2,
	}
	countPlan := &applyPatchPlan{ops: []plannedApplyPatchOp{{
		kind: "add", targetLabel: "count.txt", after: []byte("content"),
	}}}
	if err := validateApplyPatchCandidateBudgets(countCtx, countPlan); !errors.Is(err, context.Canceled) {
		t.Fatalf("budget line-count cancellation error = %v", err)
	}
}

func TestApplyPatchCandidateDiffBlockErrorCoverage(t *testing.T) {
	header := "diff --git a/file.txt b/file.txt\n"
	tests := []struct {
		name  string
		op    plannedApplyPatchOp
		limit int
	}{
		{
			name: "header", op: plannedApplyPatchOp{
				kind: "add", targetLabel: "file.txt",
			},
		},
		{
			name: "add metadata", limit: len(header), op: plannedApplyPatchOp{
				kind: "add", targetLabel: "file.txt",
			},
		},
		{
			name: "delete metadata", limit: len(header), op: plannedApplyPatchOp{
				kind: "delete", sourceLabel: "file.txt",
			},
		},
		{
			name: "update metadata", limit: len(header), op: plannedApplyPatchOp{
				kind: "update", sourceLabel: "file.txt", targetLabel: "file.txt",
			},
		},
		{
			name: "move metadata", limit: len(header), op: plannedApplyPatchOp{
				kind: "move", sourceLabel: "file.txt", targetLabel: "file.txt",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			work := applyPatchCandidateMaxDiffMatrixWork
			builder := &applyPatchCandidateBuilder{limit: test.limit}
			if err := appendApplyPatchCandidateBlock(
				context.Background(), builder, test.op, &work,
			); err == nil {
				t.Fatal("bounded block unexpectedly succeeded")
			}
		})
	}

	work := applyPatchCandidateMaxDiffMatrixWork
	if err := appendApplyPatchCandidateBlock(context.Background(),
		&applyPatchCandidateBuilder{limit: 1024}, plannedApplyPatchOp{
			kind: "add", targetLabel: "bad\x00target",
		}, &work); err == nil {
		t.Fatal("invalid source display path succeeded")
	}
	if err := appendApplyPatchCandidateBlock(context.Background(),
		&applyPatchCandidateBuilder{limit: 1024}, plannedApplyPatchOp{
			kind: "move", sourceLabel: "source", targetLabel: "bad\x00target",
		}, &work); err == nil {
		t.Fatal("invalid target display path succeeded")
	}

	addMetadata := "new file mode 100644\nrequested permissions 0644 (subject to umask)\n"
	if err := appendApplyPatchCandidateBlock(context.Background(),
		&applyPatchCandidateBuilder{limit: len(header) + len(addMetadata)},
		plannedApplyPatchOp{
			kind: "add", targetLabel: "file.txt", after: []byte("x\n"),
		}, &work); err == nil {
		t.Fatal("content-header output overflow succeeded")
	}
}

func TestApplyPatchCandidateDiffCancellationCoverage(t *testing.T) {
	op := plannedApplyPatchOp{
		kind: "update", sourceLabel: "file", targetLabel: "file",
		before: []byte("before\n"), after: []byte("after\n"), mode: 0o600,
	}
	work := applyPatchCandidateMaxDiffMatrixWork
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := appendApplyPatchCandidateBlock(
		canceled, &applyPatchCandidateBuilder{limit: 4096}, op, &work,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("block text cancellation error = %v", err)
	}

	afterTextCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 3,
	}
	if err := appendApplyPatchCandidateBlock(
		afterTextCtx, &applyPatchCandidateBuilder{limit: 4096}, op, &work,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("after-text cancellation error = %v", err)
	}

	if _, err := splitApplyPatchCandidateLines(canceled, []byte("line\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("split cancellation error = %v", err)
	}
	if _, err := digestApplyPatchCandidateContent(canceled, []byte("binary")); !errors.Is(err, context.Canceled) {
		t.Fatalf("digest cancellation error = %v", err)
	}
	if err := appendApplyPatchCandidateBinary(
		canceled, &applyPatchCandidateBuilder{limit: 4096}, []byte{0}, []byte{1},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("binary-before cancellation error = %v", err)
	}
	binaryAfterCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 3,
	}
	if err := appendApplyPatchCandidateBinary(
		binaryAfterCtx, &applyPatchCandidateBuilder{limit: 4096}, []byte{0}, []byte{1},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("binary-after cancellation error = %v", err)
	}
}

func TestApplyPatchCandidateDiffLCSAndHunkCoverage(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	work := applyPatchCandidateMaxDiffMatrixWork
	equal := []applyPatchCandidateTextLine{{text: "equal", newline: true}}
	if _, err := buildApplyPatchCandidateEdits(canceled, equal, equal, &work); !errors.Is(err, context.Canceled) {
		t.Fatalf("prefix cancellation error = %v", err)
	}
	work = applyPatchCandidateMaxDiffMatrixWork
	if _, err := buildApplyPatchCandidateEdits(canceled,
		[]applyPatchCandidateTextLine{{text: "left"}, {text: "suffix"}},
		[]applyPatchCandidateTextLine{{text: "right"}, {text: "suffix"}},
		&work,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("suffix cancellation error = %v", err)
	}

	left := make([]applyPatchCandidateTextLine, 64)
	right := make([]applyPatchCandidateTextLine, 64)
	for index := range left {
		left[index].text = fmt.Sprintf("left-%d", index)
		right[index].text = fmt.Sprintf("right-%d", index)
	}
	work = applyPatchCandidateMaxDiffMatrixWork
	if _, err := buildApplyPatchCandidateEdits(canceled, left, right, &work); !errors.Is(err, context.Canceled) {
		t.Fatalf("matrix cancellation error = %v", err)
	}
	work = applyPatchCandidateMaxDiffMatrixWork
	if _, err := buildApplyPatchCandidateEdits(canceled,
		[]applyPatchCandidateTextLine{{text: "left"}},
		[]applyPatchCandidateTextLine{{text: "right"}},
		&work,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("reconstruction cancellation error = %v", err)
	}

	work = applyPatchCandidateMaxDiffMatrixWork
	edits, err := buildApplyPatchCandidateEdits(context.Background(),
		[]applyPatchCandidateTextLine{{text: "left"}, {text: "middle"}, {text: "tail-left"}},
		[]applyPatchCandidateTextLine{{text: "right"}, {text: "middle"}, {text: "tail-right"}},
		&work,
	)
	if err != nil || len(edits) == 0 {
		t.Fatalf("middle-equality edits = %#v, %v", edits, err)
	}

	if err := appendApplyPatchCandidateHunks(
		canceled, &applyPatchCandidateBuilder{limit: 4096}, edits,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("hunk cancellation error = %v", err)
	}
	if err := appendApplyPatchCandidateHunks(context.Background(),
		&applyPatchCandidateBuilder{limit: 4096},
		[]applyPatchCandidateEditLine{{kind: ' ', line: applyPatchCandidateTextLine{text: "same"}}},
	); err != nil {
		t.Fatalf("unchanged hunk error = %v", err)
	}
	if err := appendApplyPatchCandidateHunks(context.Background(),
		&applyPatchCandidateBuilder{},
		[]applyPatchCandidateEditLine{{kind: '-', line: applyPatchCandidateTextLine{text: "x"}}},
	); err == nil {
		t.Fatal("hunk header overflow succeeded")
	}
	header := "@@ -1 +0,0 @@\n-x\n"
	if err := appendApplyPatchCandidateHunks(context.Background(),
		&applyPatchCandidateBuilder{limit: len(header)},
		[]applyPatchCandidateEditLine{{kind: '-', line: applyPatchCandidateTextLine{text: "x"}}},
	); err == nil {
		t.Fatal("no-newline marker overflow succeeded")
	}

	separated := make([]applyPatchCandidateEditLine, 12)
	for index := range separated {
		separated[index] = applyPatchCandidateEditLine{
			kind: ' ', line: applyPatchCandidateTextLine{text: fmt.Sprintf("line-%d", index), newline: true},
		}
	}
	separated[0].kind = '-'
	separated[11].kind = '+'
	if err := appendApplyPatchCandidateHunks(context.Background(),
		&applyPatchCandidateBuilder{limit: 4096}, separated,
	); err != nil {
		t.Fatalf("separated hunk groups error = %v", err)
	}
}

func TestApplyPatchCandidateDiffBinaryUnchangedCoverage(t *testing.T) {
	content := []byte{0, 1, 2}
	result, err := buildApplyPatchCandidateResult(context.Background(), &applyPatchPlan{
		ops: []plannedApplyPatchOp{{
			kind: "update", sourceLabel: "same.bin", targetLabel: "same.bin",
			before: content, after: append([]byte(nil), content...), mode: 0o600,
		}},
		summaries: []string{"updated same.bin"},
	})
	if err != nil || !strings.Contains(result.ForUser, "binary before size 3") {
		t.Fatalf("unchanged binary candidate = %#v, %v", result, err)
	}
	textResult, err := buildApplyPatchCandidateResult(context.Background(), &applyPatchPlan{
		ops: []plannedApplyPatchOp{{
			kind: "update", sourceLabel: "same.txt", targetLabel: "same.txt",
			before: []byte("same\n"), after: []byte("same\n"), mode: 0o600,
		}},
		summaries: []string{"updated same.txt"},
	})
	if err != nil || !strings.Contains(textResult.ForUser, "content unchanged") {
		t.Fatalf("unchanged text candidate = %#v, %v", textResult, err)
	}
}

func TestApplyPatchCandidateDiffLateCancellationCoverage(t *testing.T) {
	op := plannedApplyPatchOp{
		kind: "update", sourceLabel: "late.txt", targetLabel: "late.txt",
		before: []byte("before\n"), after: []byte("after\n"), mode: 0o600,
	}
	for _, remaining := range []int{5, 7, 9} {
		ctx := &applyPatchCancelAfterChecksContext{
			Context: context.Background(), remaining: remaining,
		}
		work := applyPatchCandidateMaxDiffMatrixWork
		if err := appendApplyPatchCandidateBlock(
			ctx, &applyPatchCandidateBuilder{limit: 4096}, op, &work,
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("remaining=%d cancellation error = %v", remaining, err)
		}
	}
}

func TestApplyPatchCandidateDiffSeparatorOverflowCoverage(t *testing.T) {
	probe := &applyPatchCandidateBuilder{limit: applyPatchCandidateMaxResultBytes}
	work := applyPatchCandidateMaxDiffMatrixWork
	probeOp := plannedApplyPatchOp{
		kind: "add", targetLabel: "first.txt", after: []byte("x\n"),
	}
	if err := appendApplyPatchCandidateBlock(context.Background(), probe, probeOp, &work); err != nil {
		t.Fatal(err)
	}
	lineBytes := 1 + applyPatchCandidateMaxResultBytes - len(probe.String())
	first := plannedApplyPatchOp{
		kind: "add", targetLabel: "first.txt",
		after: []byte(strings.Repeat("x", lineBytes) + "\n"),
	}
	plan := &applyPatchPlan{
		ops:       []plannedApplyPatchOp{first, {kind: "add", targetLabel: "second.txt"}},
		summaries: []string{"added first.txt", "added second.txt"},
	}
	if _, err := buildApplyPatchCandidateResult(context.Background(), plan); err == nil {
		t.Fatal("separator overflow candidate succeeded")
	}
}

func TestApplyPatchCandidateDiffCodexAdaptationSurfaceCoverage(t *testing.T) {
	codex := ToolAdaptationDecision{
		Enabled: true, VisibleToolSurface: config.ToolSurfaceCodex,
	}
	if !codex.UsesCodexCompatibleTools() || !codex.MayUseCodexCompatibleTools() {
		t.Fatal("Codex surface did not retain apply-patch-compatible admission")
	}
	for _, decision := range []ToolAdaptationDecision{
		{},
		{Enabled: true},
		{
			Enabled: true, RuntimePromotion: true,
			ApplyVisibleChanges: config.ToolVisibleChangeNever,
		},
		{
			Enabled: true, RuntimePromotion: true,
			ApplyVisibleChanges: config.ToolVisibleChangeImmediate,
			SurfaceEvidence:     "config",
		},
	} {
		if decision.UsesCodexCompatibleTools() || decision.MayUseCodexCompatibleTools() {
			t.Fatalf("non-Codex decision admitted compatibility tools: %#v", decision)
		}
	}
	promoted := ToolAdaptationDecision{
		Enabled: true, RuntimePromotion: true,
		ApplyVisibleChanges: config.ToolVisibleChangeContextBoundary,
		SurfaceEvidence:     "heuristic",
	}
	if !promoted.MayUseCodexCompatibleTools() {
		t.Fatal("safe runtime promotion did not admit compatibility tools")
	}
	if !resolveRuntimeAdaptation(config.ToolRuntimeAdaptationAllow, true) ||
		resolveRuntimeAdaptation(config.ToolRuntimeAdaptationNever, false) ||
		resolveRuntimeAdaptation(config.ToolRuntimeAdaptationAuto, true) ||
		!resolveRuntimeAdaptation(config.ToolRuntimeAdaptationAuto, false) {
		t.Fatal("runtime adaptation policy projection is inconsistent")
	}
	disabledConfig := config.DefaultToolAdaptationConfig()
	disabledConfig.Enabled = false
	disabled := ResolveToolAdaptation(
		disabledConfig, "disabled-provider", "disabled-model",
	)
	if disabled.Enabled || disabled.SurfaceEvidence != "disabled" {
		t.Fatalf("disabled adaptation decision = %#v", disabled)
	}
	cacheBreaking := config.DefaultToolAdaptationConfig()
	cacheBreaking.VisibleToolSurface = config.ToolSurfacePicoClaw
	cacheBreaking.CacheBreakingDowngrade = true
	decision := ResolveToolAdaptation(
		cacheBreaking, "coverage-provider", "coverage-model",
	)
	if !decision.RuntimeDowngrade {
		t.Fatalf("cache-breaking downgrade decision = %#v", decision)
	}
	if surface, evidence := resolveAutoToolSurface(
		config.ToolSurfaceAuto, "anthropic", "model", nil,
	); surface != config.ToolSurfaceSimple || evidence != "heuristic" {
		t.Fatalf("Anthropic auto surface = %q, %q", surface, evidence)
	}
	if surface, ok := learnedToolSurface([]ToolAdaptationToolOutcome{{
		VisibleToolSurface: config.ToolSurfaceAuto, Successes: 1,
	}}); ok || surface != "" {
		t.Fatalf("auto learned surface = %q, %t", surface, ok)
	}
}

func TestApplyPatchCandidateDiffFilesystemFacadeCompatibilityCoverage(t *testing.T) {
	workspace := t.TempDir()
	adjacent := []Tool{
		NewReadFileTool(workspace, true, 1024),
		NewReadFileBytesTool(workspace, true, 1024),
		NewWriteFileTool(workspace, true),
		NewListDirTool(workspace, true),
		NewEditFileTool(workspace, true),
		NewAppendFileTool(workspace, true),
	}
	for index, tool := range adjacent {
		if tool == nil || tool.Name() == "" {
			t.Fatalf("adjacent filesystem tool %d = %#v", index, tool)
		}
	}
}
