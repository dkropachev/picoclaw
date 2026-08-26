package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func requireApplyPatchCandidateFailure(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func requireApplyPatchCandidateExecuteFailure(
	t *testing.T,
	result *ToolResult,
	want string,
) {
	t.Helper()
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, want) {
		t.Fatalf("result = %#v, want error containing %q", result, want)
	}
	if result.ForUser != "" {
		t.Fatalf("failed patch exposed candidate diff: %q", result.ForUser)
	}
}

func TestApplyPatchCandidateDiffMixedExactOutput(t *testing.T) {
	plan := &applyPatchPlan{
		ops: []plannedApplyPatchOp{
			{
				kind: "add", targetLabel: "new.txt",
				after: []byte("alpha\nbeta\n"),
			},
			{
				kind: "update", sourceLabel: "script.sh", targetLabel: "script.sh",
				before: []byte("one\ntwo\nthree\n"), after: []byte("one\nTWO\nthree\n"),
				mode: 0o751,
			},
			{
				kind: "delete", sourceLabel: "obsolete.sh",
				before: []byte("gone\n"), mode: 0o751,
			},
			{
				kind: "move", sourceLabel: "old/name.txt", targetLabel: "new/name.txt",
				before: []byte("before\n"), after: []byte("after\n"), mode: 0o600,
			},
		},
		summaries: []string{
			"added new.txt",
			"updated script.sh",
			"deleted obsolete.sh",
			"moved old/name.txt to new/name.txt",
		},
	}

	result, err := buildApplyPatchCandidateResult(context.Background(), plan)
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	wantLLM := "added new.txt\nupdated script.sh\ndeleted obsolete.sh\n" +
		"moved old/name.txt to new/name.txt"
	if result.ForLLM != wantLLM {
		t.Fatalf("ForLLM = %q, want %q", result.ForLLM, wantLLM)
	}
	wantUser := "Patch applied\n```diff\n" +
		"diff --git a/new.txt b/new.txt\n" +
		"new file mode 100644\n" +
		"requested permissions 0644 (subject to umask)\n" +
		"--- /dev/null\n" +
		"+++ b/new.txt\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+alpha\n" +
		"+beta\n" +
		"\n" +
		"diff --git a/script.sh b/script.sh\n" +
		"file permissions 0751\n" +
		"--- a/script.sh\n" +
		"+++ b/script.sh\n" +
		"@@ -1,3 +1,3 @@\n" +
		" one\n" +
		"-two\n" +
		"+TWO\n" +
		" three\n" +
		"\n" +
		"diff --git a/obsolete.sh b/obsolete.sh\n" +
		"deleted file mode 100755\n" +
		"deleted file permissions 0751\n" +
		"--- a/obsolete.sh\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n" +
		"-gone\n" +
		"\n" +
		"diff --git a/old/name.txt b/new/name.txt\n" +
		"rename from old/name.txt\n" +
		"rename to new/name.txt\n" +
		"source permissions 0600\n" +
		"legacy target requested permissions 0644 (subject to umask; source-mode preservation deferred to P011b)\n" +
		"--- a/old/name.txt\n" +
		"+++ b/new/name.txt\n" +
		"@@ -1 +1 @@\n" +
		"-before\n" +
		"+after\n" +
		"```"
	if result.ForUser != wantUser {
		t.Fatalf("ForUser mismatch\n got: %q\nwant: %q", result.ForUser, wantUser)
	}
	if result.IsError || result.Silent || result.Async || result.ResponseHandled {
		t.Fatalf("unexpected result flags: %#v", result)
	}
}

func TestApplyPatchCandidateDiffEmptyAndFinalNewlineTransitions(t *testing.T) {
	plan := &applyPatchPlan{
		ops: []plannedApplyPatchOp{
			{kind: "add", targetLabel: "empty.txt"},
			{
				kind: "update", sourceLabel: "drop-newline.txt",
				before: []byte("same\n"), after: []byte("same"), mode: 0o640,
			},
			{
				kind: "update", sourceLabel: "add-newline.txt",
				before: []byte("same"), after: []byte("same\n"), mode: 0o600,
			},
		},
		summaries: []string{"added empty.txt", "updated drop-newline.txt", "updated add-newline.txt"},
	}
	result, err := buildApplyPatchCandidateResult(context.Background(), plan)
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	want := "Patch applied\n```diff\n" +
		"diff --git a/empty.txt b/empty.txt\n" +
		"new file mode 100644\n" +
		"requested permissions 0644 (subject to umask)\n" +
		"--- /dev/null\n+++ b/empty.txt\n" +
		"\n" +
		"diff --git a/drop-newline.txt b/drop-newline.txt\n" +
		"file permissions 0640\n" +
		"--- a/drop-newline.txt\n+++ b/drop-newline.txt\n" +
		"@@ -1 +1 @@\n" +
		"-same\n+same\n" + applyPatchCandidateNoNewlineMarker + "\n" +
		"\n" +
		"diff --git a/add-newline.txt b/add-newline.txt\n" +
		"file permissions 0600\n" +
		"--- a/add-newline.txt\n+++ b/add-newline.txt\n" +
		"@@ -1 +1 @@\n" +
		"-same\n" + applyPatchCandidateNoNewlineMarker + "\n+same\n" +
		"```"
	if result.ForUser != want {
		t.Fatalf("ForUser mismatch\n got: %q\nwant: %q", result.ForUser, want)
	}
}

func TestApplyPatchCandidateDiffBinaryDigests(t *testing.T) {
	before := []byte{0x00, 0x01, 0x02}
	after := []byte{0xff, 0x03}
	plan := &applyPatchPlan{
		ops: []plannedApplyPatchOp{{
			kind: "update", sourceLabel: "blob.bin",
			before: before, after: after, mode: 0o640,
		}},
		summaries: []string{"updated blob.bin"},
	}
	result, err := buildApplyPatchCandidateResult(context.Background(), plan)
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	beforeDigest := sha256.Sum256(before)
	afterDigest := sha256.Sum256(after)
	want := fmt.Sprintf(
		"Patch applied\n```diff\n"+
			"diff --git a/blob.bin b/blob.bin\n"+
			"file permissions 0640\n"+
			"--- a/blob.bin\n"+
			"+++ b/blob.bin\n"+
			"binary before size 3 sha256 %x\n"+
			"binary after size 2 sha256 %x\n```",
		beforeDigest,
		afterDigest,
	)
	if result.ForUser != want {
		t.Fatalf("ForUser mismatch\n got: %q\nwant: %q", result.ForUser, want)
	}
}

func TestApplyPatchCandidateDiffPreservesAndQuotesAuthoredLabels(t *testing.T) {
	sourceLabel := "/absolute old.txt"
	targetLabel := "dir/literal\\name\t\"é.txt"
	canonicalCanary := filepath.Join(t.TempDir(), "private-canonical-path")
	for _, label := range []string{sourceLabel, targetLabel} {
		display, err := applyPatchCandidateDisplayPath(label)
		if err != nil {
			t.Fatalf("display %q: %v", label, err)
		}
		if want := filepath.ToSlash(label); display != want {
			t.Fatalf("display %q = %q, want exact native slash conversion %q", label, display, want)
		}
	}
	if runtime.GOOS != "windows" {
		display, err := applyPatchCandidateDisplayPath(targetLabel)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(display, `literal\name`) {
			t.Fatalf("Unix literal backslash was normalized: %q", display)
		}
	}

	plan := &applyPatchPlan{
		ops: []plannedApplyPatchOp{{
			kind: "move", sourceLabel: sourceLabel, targetLabel: targetLabel,
			sourcePath: canonicalCanary, targetPath: canonicalCanary + "-target",
			before: []byte("unchanged\n"), after: []byte("unchanged\n"), mode: 0o600,
		}},
		summaries: []string{"moved authored labels"},
	}
	result, err := buildApplyPatchCandidateResult(context.Background(), plan)
	if err != nil {
		t.Fatalf("build candidate: %v", err)
	}
	sourceDisplay := filepath.ToSlash(sourceLabel)
	targetDisplay := filepath.ToSlash(targetLabel)
	want := "Patch applied\n```diff\n" +
		"diff --git " + strconv.Quote("a/"+sourceDisplay) + " " + strconv.Quote("b/"+targetDisplay) + "\n" +
		"rename from " + strconv.Quote(sourceDisplay) + "\n" +
		"rename to " + strconv.Quote(targetDisplay) + "\n" +
		"source permissions 0600\n" +
		"legacy target requested permissions 0644 (subject to umask; source-mode preservation deferred to P011b)\n```"
	if result.ForUser != want {
		t.Fatalf("ForUser mismatch\n got: %q\nwant: %q", result.ForUser, want)
	}
	if strings.Contains(result.ForUser, canonicalCanary) ||
		strings.Contains(result.ForUser, canonicalCanary+"-target") {
		t.Fatalf("candidate leaked a canonical implementation path: %q", result.ForUser)
	}

	for _, label := range []string{"", "bad\x00name", string([]byte{0xff})} {
		if _, err := applyPatchCandidateDisplayPath(label); err == nil {
			t.Fatalf("invalid label %q was accepted", label)
		}
	}
}

func TestApplyPatchCandidateDiffExecutePreservesAuthorizedLabels(t *testing.T) {
	t.Run("absolute allowed label", func(t *testing.T) {
		workspace := t.TempDir()
		outside := t.TempDir()
		target := filepath.Join(outside, "absolute.txt")
		allowOutside := regexp.MustCompile(
			"^" + regexp.QuoteMeta(outside) + regexp.QuoteMeta(string(os.PathSeparator)) + ".*$",
		)
		tool := newApplyPatchPreflightTestTool(
			t,
			workspace,
			true,
			true,
			ApplyPatchPreflightPolicy{},
			[]*regexp.Regexp{allowOutside},
		)
		result := executeApplyPatch(
			t,
			tool,
			context.Background(),
			"*** Begin Patch\n*** Add File: "+target+"\n+outside\n*** End Patch",
		)
		if result.IsError {
			t.Fatalf("absolute allowed patch failed: %s", result.ForLLM)
		}
		display := filepath.ToSlash(target)
		if !strings.Contains(result.ForUser, "diff --git a/"+display+" b/"+display+"\n") {
			t.Fatalf("absolute authored label was rewritten: %q", result.ForUser)
		}
		if content, err := os.ReadFile(target); err != nil || string(content) != "outside\n" {
			t.Fatalf("absolute target content = %q, %v", content, err)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("Unix literal backslash", func(t *testing.T) {
			workspace := t.TempDir()
			const label = `literal\name.txt`
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(
				t,
				tool,
				context.Background(),
				"*** Begin Patch\n*** Add File: "+label+"\n+literal\n*** End Patch",
			)
			if result.IsError {
				t.Fatalf("literal-backslash patch failed: %s", result.ForLLM)
			}
			if !strings.Contains(result.ForUser, strconv.Quote("a/"+label)) ||
				!strings.Contains(result.ForUser, strconv.Quote("b/"+label)) {
				t.Fatalf("literal backslash was rewritten: %q", result.ForUser)
			}
			if got := readApplyPatchFixture(t, workspace, label); got != "literal\n" {
				t.Fatalf("literal-backslash content = %q", got)
			}
		})
	}
}

func TestApplyPatchCandidateDiffDynamicFenceAndDeterminism(t *testing.T) {
	plan := &applyPatchPlan{
		ops: []plannedApplyPatchOp{{
			kind: "add", targetLabel: "ticks.txt", after: []byte("prefix ````` suffix\n"),
		}},
		summaries: []string{"added ticks.txt"},
	}
	first, err := buildApplyPatchCandidateResult(context.Background(), plan)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if !strings.HasPrefix(first.ForUser, "Patch applied\n``````diff\n") ||
		!strings.HasSuffix(first.ForUser, "\n``````") {
		t.Fatalf("dynamic fence did not exceed content run: %q", first.ForUser)
	}
	for iteration := 0; iteration < 20; iteration++ {
		next, buildErr := buildApplyPatchCandidateResult(context.Background(), plan)
		if buildErr != nil {
			t.Fatalf("build %d: %v", iteration, buildErr)
		}
		if next.ForLLM != first.ForLLM || next.ForUser != first.ForUser ||
			next.Silent != first.Silent || next.IsError != first.IsError ||
			next.Async != first.Async || next.ResponseHandled != first.ResponseHandled {
			t.Fatalf("build %d was nondeterministic\nfirst=%#v\n next=%#v", iteration, first, next)
		}
	}
}

func TestApplyPatchCandidateDiffBudgetBoundaries(t *testing.T) {
	makeAdds := func(count, labelBytes int) []plannedApplyPatchOp {
		ops := make([]plannedApplyPatchOp, count)
		for index := range ops {
			ops[index] = plannedApplyPatchOp{
				kind: "add", targetLabel: strings.Repeat("p", labelBytes),
			}
		}
		return ops
	}

	t.Run("operations", func(t *testing.T) {
		if err := validateApplyPatchCandidateBudgets(context.Background(), &applyPatchPlan{
			ops: makeAdds(applyPatchCandidateMaxOperations, 1),
		}); err != nil {
			t.Fatalf("inclusive operation limit failed: %v", err)
		}
		err := validateApplyPatchCandidateBudgets(context.Background(), &applyPatchPlan{
			ops: makeAdds(applyPatchCandidateMaxOperations+1, 1),
		})
		requireApplyPatchCandidateFailure(t, err, "operation limit")
	})

	t.Run("input bytes", func(t *testing.T) {
		exact := bytes.Repeat([]byte{0}, applyPatchCandidateMaxInputBytes)
		plan := &applyPatchPlan{ops: []plannedApplyPatchOp{{
			kind: "update", sourceLabel: "file", before: exact,
		}}}
		if err := validateApplyPatchCandidateBudgets(context.Background(), plan); err != nil {
			t.Fatalf("inclusive byte limit failed: %v", err)
		}
		plan.ops[0].before = append(exact, 0)
		requireApplyPatchCandidateFailure(
			t,
			validateApplyPatchCandidateBudgets(context.Background(), plan),
			"input byte limit",
		)
	})

	t.Run("input lines", func(t *testing.T) {
		exact := bytes.Repeat([]byte{'\n'}, applyPatchCandidateMaxInputLines)
		plan := &applyPatchPlan{ops: []plannedApplyPatchOp{{
			kind: "update", sourceLabel: "file", before: exact,
		}}}
		if err := validateApplyPatchCandidateBudgets(context.Background(), plan); err != nil {
			t.Fatalf("inclusive line limit failed: %v", err)
		}
		plan.ops[0].before = append(exact, 'x')
		requireApplyPatchCandidateFailure(
			t,
			validateApplyPatchCandidateBudgets(context.Background(), plan),
			"input line limit",
		)
	})

	t.Run("per label", func(t *testing.T) {
		plan := &applyPatchPlan{ops: makeAdds(1, applyPatchCandidateMaxPathBytes)}
		if err := validateApplyPatchCandidateBudgets(context.Background(), plan); err != nil {
			t.Fatalf("inclusive label limit failed: %v", err)
		}
		plan.ops[0].targetLabel += "p"
		requireApplyPatchCandidateFailure(
			t,
			validateApplyPatchCandidateBudgets(context.Background(), plan),
			"path limit",
		)
	})

	t.Run("aggregate labels", func(t *testing.T) {
		labelBytes := applyPatchCandidateMaxAllPathBytes / applyPatchCandidateMaxOperations
		plan := &applyPatchPlan{ops: makeAdds(applyPatchCandidateMaxOperations, labelBytes)}
		if err := validateApplyPatchCandidateBudgets(context.Background(), plan); err != nil {
			t.Fatalf("inclusive aggregate label limit failed: %v", err)
		}
		plan.ops[len(plan.ops)-1].targetLabel += "p"
		requireApplyPatchCandidateFailure(
			t,
			validateApplyPatchCandidateBudgets(context.Background(), plan),
			"aggregate path limit",
		)
	})

	t.Run("quoted label counting", func(t *testing.T) {
		exactLabel := strings.Repeat("p", applyPatchCandidateMaxPathBytes-4) + "\t"
		quoted, err := quoteApplyPatchCandidatePath("", exactLabel)
		if err != nil {
			t.Fatal(err)
		}
		if len(quoted) != applyPatchCandidateMaxPathBytes {
			t.Fatalf("quoted boundary length = %d, want %d", len(quoted), applyPatchCandidateMaxPathBytes)
		}
		plan := &applyPatchPlan{ops: []plannedApplyPatchOp{{kind: "add", targetLabel: exactLabel}}}
		if err := validateApplyPatchCandidateBudgets(context.Background(), plan); err != nil {
			t.Fatalf("inclusive quoted label limit failed: %v", err)
		}
		plan.ops[0].targetLabel = "p" + exactLabel
		requireApplyPatchCandidateFailure(
			t,
			validateApplyPatchCandidateBudgets(context.Background(), plan),
			"path limit",
		)

		aggregateLabel := strings.Repeat("p", 252) + "\t"
		aggregate := make([]plannedApplyPatchOp, applyPatchCandidateMaxOperations)
		for index := range aggregate {
			aggregate[index] = plannedApplyPatchOp{kind: "add", targetLabel: aggregateLabel}
		}
		plan.ops = aggregate
		if err := validateApplyPatchCandidateBudgets(context.Background(), plan); err != nil {
			t.Fatalf("inclusive quoted aggregate limit failed: %v", err)
		}
		plan.ops[len(plan.ops)-1].targetLabel = "p" + aggregateLabel
		requireApplyPatchCandidateFailure(
			t,
			validateApplyPatchCandidateBudgets(context.Background(), plan),
			"aggregate path limit",
		)
	})

	t.Run("complete output", func(t *testing.T) {
		makePlan := func(lineBytes int) *applyPatchPlan {
			return &applyPatchPlan{
				ops: []plannedApplyPatchOp{{
					kind: "add", targetLabel: "x", after: []byte(strings.Repeat("x", lineBytes) + "\n"),
				}},
				summaries: []string{"added x"},
			}
		}
		baseline, err := buildApplyPatchCandidateResult(context.Background(), makePlan(1))
		if err != nil {
			t.Fatalf("baseline output: %v", err)
		}
		exactLineBytes := 1 + applyPatchCandidateMaxResultBytes - len(baseline.ForUser)
		exact, err := buildApplyPatchCandidateResult(context.Background(), makePlan(exactLineBytes))
		if err != nil {
			t.Fatalf("inclusive output limit failed: %v", err)
		}
		if len(exact.ForUser) != applyPatchCandidateMaxResultBytes {
			t.Fatalf("inclusive output length = %d, want %d", len(exact.ForUser), applyPatchCandidateMaxResultBytes)
		}
		_, err = buildApplyPatchCandidateResult(context.Background(), makePlan(exactLineBytes+1))
		requireApplyPatchCandidateFailure(t, err, "output limit")
	})

	t.Run("builder output", func(t *testing.T) {
		builder := &applyPatchCandidateBuilder{limit: applyPatchCandidateMaxResultBytes}
		if err := builder.append(strings.Repeat("x", applyPatchCandidateMaxResultBytes)); err != nil {
			t.Fatalf("inclusive builder output limit failed: %v", err)
		}
		requireApplyPatchCandidateFailure(t, builder.append("x"), "output limit")
		if len(builder.String()) != applyPatchCandidateMaxResultBytes {
			t.Fatalf("failed append changed builder length: %d", len(builder.String()))
		}
	})
}

func TestApplyPatchCandidateDiffLCSWorkBoundary(t *testing.T) {
	const sideLines = 1_999
	before := make([]applyPatchCandidateTextLine, sideLines)
	after := make([]applyPatchCandidateTextLine, sideLines)
	for index := range before {
		before[index] = applyPatchCandidateTextLine{text: fmt.Sprintf("left-%04d", index), newline: true}
		after[index] = applyPatchCandidateTextLine{text: fmt.Sprintf("right-%04d", index), newline: true}
	}
	work := applyPatchCandidateMaxDiffMatrixWork
	edits, err := buildApplyPatchCandidateEdits(context.Background(), before, after, &work)
	if err != nil {
		t.Fatalf("inclusive LCS work limit failed: %v", err)
	}
	if work != 0 || len(edits) != len(before)+len(after) {
		t.Fatalf("inclusive LCS result: work=%d edits=%d", work, len(edits))
	}
	work = applyPatchCandidateMaxDiffMatrixWork - 1
	_, err = buildApplyPatchCandidateEdits(context.Background(), before, after, &work)
	requireApplyPatchCandidateFailure(t, err, "work limit")

	smallBefore := []applyPatchCandidateTextLine{{text: "before"}}
	smallAfter := []applyPatchCandidateTextLine{{text: "after"}}
	work = 8
	for operation := 0; operation < 2; operation++ {
		if _, buildErr := buildApplyPatchCandidateEdits(
			context.Background(), smallBefore, smallAfter, &work,
		); buildErr != nil {
			t.Fatalf("aggregate LCS operation %d failed: %v", operation, buildErr)
		}
	}
	if work != 0 {
		t.Fatalf("aggregate LCS work remaining = %d, want 0", work)
	}
	_, err = buildApplyPatchCandidateEdits(
		context.Background(), smallBefore, smallAfter, &work,
	)
	requireApplyPatchCandidateFailure(t, err, "work limit")
}

func TestApplyPatchCandidateDiffHunkGroupingRangesAndTieBreak(t *testing.T) {
	buildGap := func(t *testing.T, gap int) string {
		t.Helper()
		var before strings.Builder
		var after strings.Builder
		before.WriteString("old-first\n")
		after.WriteString("new-first\n")
		for index := 0; index < gap; index++ {
			line := fmt.Sprintf("context-%d\n", index)
			before.WriteString(line)
			after.WriteString(line)
		}
		before.WriteString("old-last\n")
		after.WriteString("new-last\n")
		result, err := buildApplyPatchCandidateResult(context.Background(), &applyPatchPlan{
			ops: []plannedApplyPatchOp{{
				kind: "update", sourceLabel: "group.txt",
				before: []byte(before.String()), after: []byte(after.String()), mode: 0o640,
			}},
			summaries: []string{"updated group.txt"},
		})
		if err != nil {
			t.Fatalf("gap %d: %v", gap, err)
		}
		return result.ForUser
	}

	merged := buildGap(t, 6)
	if strings.Count(merged, "@@ ") != 1 || !strings.Contains(merged, "@@ -1,8 +1,8 @@") {
		t.Fatalf("six unchanged lines did not merge hunks: %q", merged)
	}
	split := buildGap(t, 7)
	if strings.Count(split, "@@ ") != 2 ||
		!strings.Contains(split, "@@ -1,4 +1,4 @@") ||
		!strings.Contains(split, "@@ -6,4 +6,4 @@") {
		t.Fatalf("seven unchanged lines did not split with exact ranges: %q", split)
	}

	for _, test := range []struct {
		name   string
		before string
		after  string
		order  string
	}{
		{name: "insert at beginning", before: "tail\n", after: "head\ntail\n", order: "+head\n tail\n"},
		{name: "insert at end", before: "head\n", after: "head\ntail\n", order: " head\n+tail\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := buildApplyPatchCandidateResult(context.Background(), &applyPatchPlan{
				ops: []plannedApplyPatchOp{{
					kind: "update", sourceLabel: "edge.txt",
					before: []byte(test.before), after: []byte(test.after), mode: 0o600,
				}},
				summaries: []string{"updated edge.txt"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.ForUser, "@@ -1 +1,2 @@\n"+test.order) {
				t.Fatalf("edge hunk has wrong range/order: %q", result.ForUser)
			}
		})
	}

	work := 4
	edits, err := buildApplyPatchCandidateEdits(
		context.Background(),
		[]applyPatchCandidateTextLine{{text: "old", newline: true}},
		[]applyPatchCandidateTextLine{{text: "new", newline: true}},
		&work,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 || edits[0].kind != '-' || edits[1].kind != '+' {
		t.Fatalf("LCS tie was not deterministic delete-before-add: %#v", edits)
	}
}

func TestApplyPatchCandidateDiffDefensiveAndCancellationPaths(t *testing.T) {
	for _, plan := range []*applyPatchPlan{nil, {}} {
		_, err := buildApplyPatchCandidateResult(context.Background(), plan)
		requireApplyPatchCandidateFailure(t, err, "plan is empty")
	}
	if _, err := buildApplyPatchCandidateResult(nil, &applyPatchPlan{
		ops: []plannedApplyPatchOp{{kind: "add", targetLabel: "nil-context.txt"}},
	}); err != nil {
		t.Fatalf("nil context was not normalized: %v", err)
	}
	if labels := applyPatchCandidateLogicalLabels(plannedApplyPatchOp{kind: "unknown"}); labels != nil {
		t.Fatalf("unsupported logical labels = %#v", labels)
	}
	if applyPatchCandidateGitMode(0o640) != "100644" {
		t.Fatal("non-executable Git mode was not 100644")
	}
	if _, err := quoteApplyPatchCandidatePath("a/", ""); err == nil {
		t.Fatal("empty quoted path was accepted")
	}
	work := applyPatchCandidateMaxDiffMatrixWork
	if err := appendApplyPatchCandidateBlock(
		context.Background(),
		&applyPatchCandidateBuilder{limit: applyPatchCandidateMaxResultBytes},
		plannedApplyPatchOp{kind: "unsupported", sourceLabel: "x", targetLabel: "x"},
		&work,
	); err == nil {
		t.Fatal("unsupported operation was rendered")
	}

	for _, test := range []struct {
		name string
		call func(context.Context) error
	}{
		{
			name: "validate",
			call: func(ctx context.Context) error {
				return validateApplyPatchCandidateBudgets(ctx, &applyPatchPlan{
					ops: []plannedApplyPatchOp{{kind: "add", targetLabel: "x"}},
				})
			},
		},
		{
			name: "line count",
			call: func(ctx context.Context) error {
				_, err := countApplyPatchCandidateLines(ctx, []byte("x"))
				return err
			},
		},
		{
			name: "text classification",
			call: func(ctx context.Context) error {
				_, err := isApplyPatchCandidateText(ctx, []byte("x"))
				return err
			},
		},
		{
			name: "digest",
			call: func(ctx context.Context) error {
				_, err := digestApplyPatchCandidateContent(ctx, []byte("x"))
				return err
			},
		},
		{
			name: "line split",
			call: func(ctx context.Context) error {
				_, err := splitApplyPatchCandidateLines(ctx, []byte("x"))
				return err
			},
		},
		{
			name: "LCS reconstruction",
			call: func(ctx context.Context) error {
				work := 4
				_, err := buildApplyPatchCandidateEdits(
					ctx,
					[]applyPatchCandidateTextLine{{text: "old"}},
					[]applyPatchCandidateTextLine{{text: "new"}},
					&work,
				)
				return err
			},
		},
		{
			name: "hunk render",
			call: func(ctx context.Context) error {
				return appendApplyPatchCandidateHunks(
					ctx,
					&applyPatchCandidateBuilder{limit: 128},
					[]applyPatchCandidateEditLine{{kind: '+', line: applyPatchCandidateTextLine{text: "x"}}},
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			requireApplyPatchCandidateFailure(t, test.call(ctx), context.Canceled.Error())
		})
	}

	ctx := &applyPatchCancelAfterChecksContext{Context: context.Background(), remaining: 2}
	err := appendApplyPatchCandidateBinary(
		ctx,
		&applyPatchCandidateBuilder{limit: 256},
		nil,
		[]byte{0xff},
	)
	requireApplyPatchCandidateFailure(t, err, context.Canceled.Error())
}

func TestApplyPatchCandidateDiffControlContentIsDigestOnly(t *testing.T) {
	control := []byte("private-prefix\u202eraw-secret")
	result, err := buildApplyPatchCandidateResult(context.Background(), &applyPatchPlan{
		ops: []plannedApplyPatchOp{{
			kind: "update", sourceLabel: "control.txt",
			before: control, after: []byte("safe\n"), mode: 0o600,
		}},
		summaries: []string{"updated control.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(control)
	if !strings.Contains(result.ForUser, fmt.Sprintf("sha256 %x", digest)) ||
		strings.Contains(result.ForUser, "private-prefix") ||
		strings.Contains(result.ForUser, "raw-secret") {
		t.Fatalf("control content was not represented only by digest: %q", result.ForUser)
	}
}

func TestApplyPatchCandidateDiffExecuteFailureWithholdsDiffAndMutation(t *testing.T) {
	t.Run("output budget", func(t *testing.T) {
		workspace := t.TempDir()
		before := applyPatchSnapshotTree(t, workspace)
		tool := newApplyPatchPreflightTestTool(
			t, workspace, true, true, ApplyPatchPreflightPolicy{},
		)
		patch := "*** Begin Patch\n*** Add File: huge.txt\n+" +
			strings.Repeat("x", applyPatchCandidateMaxResultBytes) +
			"\n*** End Patch"
		result := executeApplyPatch(t, tool, context.Background(), patch)
		requireApplyPatchCandidateExecuteFailure(t, result, "output limit")
		assertApplyPatchTreeEqual(t, workspace, before)
	})

	t.Run("cancellation after candidate", func(t *testing.T) {
		workspace := t.TempDir()
		writeApplyPatchFixture(t, workspace, "file.txt", "before\n", 0o640)
		before := applyPatchSnapshotTree(t, workspace)
		tool := newApplyPatchPreflightTestTool(
			t, workspace, true, true, ApplyPatchPreflightPolicy{},
		)
		ctx, cancel := context.WithCancel(context.Background())
		tool.beforeRevalidate = func(*applyPatchPlan) { cancel() }
		result := executeApplyPatch(
			t,
			tool,
			ctx,
			"*** Begin Patch\n*** Update File: file.txt\n@@\n-before\n+after\n*** End Patch",
		)
		requireApplyPatchCandidateExecuteFailure(t, result, context.Canceled.Error())
		assertApplyPatchTreeEqual(t, workspace, before)
	})

	t.Run("revalidation failure after candidate", func(t *testing.T) {
		workspace := t.TempDir()
		writeApplyPatchFixture(t, workspace, "file.txt", "before\n", 0o640)
		before := applyPatchSnapshotTree(t, workspace)
		tool := newApplyPatchPreflightTestTool(
			t, workspace, true, true, ApplyPatchPreflightPolicy{},
		)
		tool.beforeRevalidate = func(plan *applyPatchPlan) {
			plan.ops[0].source.mode ^= 0o111
		}
		result := executeApplyPatch(
			t,
			tool,
			context.Background(),
			"*** Begin Patch\n*** Update File: file.txt\n@@\n-before\n+after\n*** End Patch",
		)
		requireApplyPatchCandidateExecuteFailure(t, result, "changed during preflight")
		assertApplyPatchTreeEqual(t, workspace, before)
	})

	t.Run("commit failure after candidate", func(t *testing.T) {
		workspace := t.TempDir()
		writeApplyPatchFixture(t, workspace, "file.txt", "before\n", 0o640)
		before := applyPatchSnapshotTree(t, workspace)
		tool := newApplyPatchPreflightTestTool(
			t, workspace, true, true, ApplyPatchPreflightPolicy{},
		)
		tool.afterPointOfNoReturn = func(plan *applyPatchPlan) {
			plan.ops[0].targetPath = workspace
		}
		result := executeApplyPatch(
			t,
			tool,
			context.Background(),
			"*** Begin Patch\n*** Update File: file.txt\n@@\n-before\n+after\n*** End Patch",
		)
		requireApplyPatchCandidateExecuteFailure(t, result, "update file")
		assertApplyPatchTreeEqual(t, workspace, before)
	})
}
