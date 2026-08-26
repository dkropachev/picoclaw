package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	applyPatchCandidateMaxOperations     = 128
	applyPatchCandidateMaxInputBytes     = 1 << 20
	applyPatchCandidateMaxInputLines     = 20_000
	applyPatchCandidateMaxPathBytes      = 4 << 10
	applyPatchCandidateMaxAllPathBytes   = 32 << 10
	applyPatchCandidateMaxResultBytes    = 64 << 10
	applyPatchCandidateMaxDiffMatrixWork = 4_000_000
	applyPatchCandidateContextLines      = 3
)

const applyPatchCandidateNoNewlineMarker = `\ No newline at end of file`

type applyPatchCandidateTextLine struct {
	text    string
	newline bool
}

type applyPatchCandidateEditLine struct {
	kind byte
	line applyPatchCandidateTextLine
}

type applyPatchCandidateBuilder struct {
	text  strings.Builder
	limit int
}

func (builder *applyPatchCandidateBuilder) append(value string) error {
	if len(value) > builder.limit-builder.text.Len() {
		return fmt.Errorf("apply-patch candidate diff exceeds output limit")
	}
	_, _ = builder.text.WriteString(value)
	return nil
}

func (builder *applyPatchCandidateBuilder) String() string {
	return builder.text.String()
}

func buildApplyPatchCandidateResult(
	ctx context.Context,
	plan *applyPatchPlan,
) (*ToolResult, error) {
	if plan == nil || len(plan.ops) == 0 {
		return nil, fmt.Errorf("apply-patch candidate plan is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateApplyPatchCandidateBudgets(ctx, plan); err != nil {
		return nil, err
	}

	diff := &applyPatchCandidateBuilder{limit: applyPatchCandidateMaxResultBytes}
	matrixWork := applyPatchCandidateMaxDiffMatrixWork
	for index := range plan.ops {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if index > 0 {
			if err := diff.append("\n"); err != nil {
				return nil, err
			}
		}
		if err := appendApplyPatchCandidateBlock(
			ctx,
			diff,
			plan.ops[index],
			&matrixWork,
		); err != nil {
			return nil, err
		}
	}

	summary := strings.Join(plan.summaries, "\n")
	rawDiff := strings.TrimSuffix(diff.String(), "\n")
	fence := applyPatchCandidateFence(rawDiff)
	user := "Patch applied\n" + fence + "diff\n" + rawDiff + "\n" + fence
	if len(user) > applyPatchCandidateMaxResultBytes {
		return nil, fmt.Errorf("apply-patch candidate diff exceeds output limit")
	}
	return &ToolResult{ForLLM: summary, ForUser: user}, nil
}

func validateApplyPatchCandidateBudgets(ctx context.Context, plan *applyPatchPlan) error {
	if len(plan.ops) > applyPatchCandidateMaxOperations {
		return fmt.Errorf("apply-patch candidate diff exceeds operation limit")
	}
	totalBytes := 0
	totalLines := 0
	totalPaths := 0
	for index := range plan.ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		op := plan.ops[index]
		for _, content := range [][]byte{op.before, op.after} {
			if len(content) > applyPatchCandidateMaxInputBytes-totalBytes {
				return fmt.Errorf("apply-patch candidate diff exceeds input byte limit")
			}
			totalBytes += len(content)
			lines, err := countApplyPatchCandidateLines(ctx, content)
			if err != nil {
				return err
			}
			if lines > applyPatchCandidateMaxInputLines-totalLines {
				return fmt.Errorf("apply-patch candidate diff exceeds input line limit")
			}
			totalLines += lines
		}
		for _, label := range applyPatchCandidateLogicalLabels(op) {
			quoted, err := quoteApplyPatchCandidatePath("", label)
			if err != nil {
				return err
			}
			if len(quoted) > applyPatchCandidateMaxPathBytes {
				return fmt.Errorf("apply-patch candidate diff exceeds path limit")
			}
			if len(quoted) > applyPatchCandidateMaxAllPathBytes-totalPaths {
				return fmt.Errorf("apply-patch candidate diff exceeds aggregate path limit")
			}
			totalPaths += len(quoted)
		}
	}
	return ctx.Err()
}

func applyPatchCandidateLogicalLabels(op plannedApplyPatchOp) []string {
	switch op.kind {
	case "add":
		return []string{op.targetLabel}
	case "delete", "update":
		return []string{op.sourceLabel}
	case "move":
		return []string{op.sourceLabel, op.targetLabel}
	default:
		return nil
	}
}

func countApplyPatchCandidateLines(ctx context.Context, content []byte) (int, error) {
	const checkInterval = 64 * 1024
	lines := 0
	for start := 0; start < len(content); start += checkInterval {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		end := min(start+checkInterval, len(content))
		lines += bytes.Count(content[start:end], []byte{'\n'})
	}
	if len(content) > 0 && content[len(content)-1] != '\n' {
		lines++
	}
	return lines, ctx.Err()
}

func appendApplyPatchCandidateBlock(
	ctx context.Context,
	builder *applyPatchCandidateBuilder,
	op plannedApplyPatchOp,
	matrixWork *int,
) error {
	sourceLabel := op.sourceLabel
	targetLabel := op.targetLabel
	if sourceLabel == "" {
		sourceLabel = targetLabel
	}
	if targetLabel == "" {
		targetLabel = sourceLabel
	}
	sourceDisplay, sourcePathErr := applyPatchCandidateDisplayPath(sourceLabel)
	if sourcePathErr != nil {
		return sourcePathErr
	}
	targetDisplay, targetPathErr := applyPatchCandidateDisplayPath(targetLabel)
	if targetPathErr != nil {
		return targetPathErr
	}
	aPath := quoteApplyPatchCandidateDisplay("a/" + sourceDisplay)
	bPath := quoteApplyPatchCandidateDisplay("b/" + targetDisplay)
	if err := builder.append("diff --git " + aPath + " " + bPath + "\n"); err != nil {
		return err
	}

	switch op.kind {
	case "add":
		if err := builder.append("new file mode 100644\nrequested permissions 0644 (subject to umask)\n"); err != nil {
			return err
		}
	case "delete":
		if err := builder.append(fmt.Sprintf(
			"deleted file mode %s\ndeleted file permissions %04o\n",
			applyPatchCandidateGitMode(op.mode),
			op.mode.Perm(),
		)); err != nil {
			return err
		}
	case "update":
		if err := builder.append(fmt.Sprintf("file permissions %04o\n", op.mode.Perm())); err != nil {
			return err
		}
	case "move":
		renameFrom := quoteApplyPatchCandidateDisplay(sourceDisplay)
		renameTo := quoteApplyPatchCandidateDisplay(targetDisplay)
		if err := builder.append(
			"rename from " + renameFrom + "\nrename to " + renameTo + "\n" +
				fmt.Sprintf("source permissions %04o\n", op.mode.Perm()) +
				"legacy target requested permissions 0644 " +
				"(subject to umask; source-mode preservation deferred to P011b)\n",
		); err != nil {
			return err
		}
	default:
		return fmt.Errorf("apply-patch candidate diff has unsupported operation")
	}

	contentChanged := !bytes.Equal(op.before, op.after)
	if !contentChanged && op.kind == "move" {
		return nil
	}

	fromPath := aPath
	toPath := bPath
	if op.kind == "add" {
		fromPath = "/dev/null"
	}
	if op.kind == "delete" {
		toPath = "/dev/null"
	}
	if err := builder.append("--- " + fromPath + "\n+++ " + toPath + "\n"); err != nil {
		return err
	}
	beforeText, err := isApplyPatchCandidateText(ctx, op.before)
	if err != nil {
		return err
	}
	afterText, err := isApplyPatchCandidateText(ctx, op.after)
	if err != nil {
		return err
	}
	if !beforeText || !afterText {
		return appendApplyPatchCandidateBinary(ctx, builder, op.before, op.after)
	}
	if !contentChanged {
		if op.kind == "update" {
			return builder.append("content unchanged\n")
		}
		return nil
	}
	beforeLines, err := splitApplyPatchCandidateLines(ctx, op.before)
	if err != nil {
		return err
	}
	afterLines, err := splitApplyPatchCandidateLines(ctx, op.after)
	if err != nil {
		return err
	}
	edits, err := buildApplyPatchCandidateEdits(
		ctx,
		beforeLines,
		afterLines,
		matrixWork,
	)
	if err != nil {
		return err
	}
	return appendApplyPatchCandidateHunks(ctx, builder, edits)
}

func applyPatchCandidateGitMode(mode os.FileMode) string {
	if mode.Perm()&0o111 != 0 {
		return "100755"
	}
	return "100644"
}

func applyPatchCandidateDisplayPath(label string) (string, error) {
	if label == "" || !utf8.ValidString(label) || strings.ContainsRune(label, '\x00') {
		return "", fmt.Errorf("apply-patch candidate diff path is invalid")
	}
	display := filepath.ToSlash(label)
	return display, nil
}

func quoteApplyPatchCandidatePath(prefix, label string) (string, error) {
	display, err := applyPatchCandidateDisplayPath(label)
	if err != nil {
		return "", err
	}
	return quoteApplyPatchCandidateDisplay(prefix + display), nil
}

func quoteApplyPatchCandidateDisplay(value string) string {
	for _, character := range value {
		if character <= 0x20 || character >= 0x7f || character == '"' || character == '\\' {
			return strconv.Quote(value)
		}
	}
	return value
}

func isApplyPatchCandidateText(ctx context.Context, content []byte) (bool, error) {
	if !utf8.Valid(content) {
		return false, nil
	}
	for index, character := range string(content) {
		if index%(64*1024) == 0 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		if character == '\n' || character == '\t' {
			continue
		}
		if unicode.IsControl(character) || character == '\u2028' || character == '\u2029' ||
			character == '\u061c' || character == '\u200e' || character == '\u200f' ||
			character >= '\u202a' && character <= '\u202e' ||
			character >= '\u2066' && character <= '\u2069' {
			return false, nil
		}
	}
	return true, ctx.Err()
}

func appendApplyPatchCandidateBinary(
	ctx context.Context,
	builder *applyPatchCandidateBuilder,
	before, after []byte,
) error {
	beforeDigest, err := digestApplyPatchCandidateContent(ctx, before)
	if err != nil {
		return err
	}
	afterDigest, err := digestApplyPatchCandidateContent(ctx, after)
	if err != nil {
		return err
	}
	return builder.append(fmt.Sprintf(
		"binary before size %d sha256 %x\nbinary after size %d sha256 %x\n",
		len(before),
		beforeDigest,
		len(after),
		afterDigest,
	))
}

func digestApplyPatchCandidateContent(ctx context.Context, content []byte) ([32]byte, error) {
	const chunkSize = 64 * 1024
	hash := sha256.New()
	for start := 0; start < len(content); start += chunkSize {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		end := min(start+chunkSize, len(content))
		_, _ = hash.Write(content[start:end])
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, ctx.Err()
}

func splitApplyPatchCandidateLines(
	ctx context.Context,
	content []byte,
) ([]applyPatchCandidateTextLine, error) {
	if len(content) == 0 {
		return nil, ctx.Err()
	}
	lines := make([]applyPatchCandidateTextLine, 0, bytes.Count(content, []byte{'\n'})+1)
	start := 0
	for index, value := range content {
		if index%(64*1024) == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if value != '\n' {
			continue
		}
		lines = append(lines, applyPatchCandidateTextLine{
			text: string(content[start:index]), newline: true,
		})
		start = index + 1
	}
	if start < len(content) {
		lines = append(lines, applyPatchCandidateTextLine{text: string(content[start:])})
	}
	return lines, ctx.Err()
}

func applyPatchCandidateLinesEqual(left, right applyPatchCandidateTextLine) bool {
	return left.newline == right.newline && left.text == right.text
}

func buildApplyPatchCandidateEdits(
	ctx context.Context,
	before, after []applyPatchCandidateTextLine,
	matrixWork *int,
) ([]applyPatchCandidateEditLine, error) {
	prefix := 0
	for prefix < len(before) && prefix < len(after) &&
		applyPatchCandidateLinesEqual(before[prefix], after[prefix]) {
		if prefix%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		applyPatchCandidateLinesEqual(
			before[len(before)-suffix-1],
			after[len(after)-suffix-1],
		) {
		if suffix%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		suffix++
	}

	left := before[prefix : len(before)-suffix]
	right := after[prefix : len(after)-suffix]
	rows, columns := len(left)+1, len(right)+1
	if columns > *matrixWork || rows > *matrixWork/columns {
		return nil, fmt.Errorf("apply-patch candidate diff exceeds work limit")
	}
	cells := rows * columns
	*matrixWork -= cells
	matrix := make([]uint32, cells)
	checks := 0
	for row := len(left) - 1; row >= 0; row-- {
		for column := len(right) - 1; column >= 0; column-- {
			checks++
			if checks%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			index := row*columns + column
			if applyPatchCandidateLinesEqual(left[row], right[column]) {
				matrix[index] = matrix[(row+1)*columns+column+1] + 1
			} else {
				matrix[index] = max(
					matrix[(row+1)*columns+column],
					matrix[row*columns+column+1],
				)
			}
		}
	}

	edits := make([]applyPatchCandidateEditLine, 0, len(before)+len(after))
	for index := 0; index < prefix; index++ {
		edits = append(edits, applyPatchCandidateEditLine{kind: ' ', line: before[index]})
	}
	row, column := 0, 0
	for row < len(left) || column < len(right) {
		if (row+column)%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		switch {
		case row < len(left) && column < len(right) &&
			applyPatchCandidateLinesEqual(left[row], right[column]):
			edits = append(edits, applyPatchCandidateEditLine{kind: ' ', line: left[row]})
			row++
			column++
		case row < len(left) && (column == len(right) ||
			matrix[(row+1)*columns+column] >= matrix[row*columns+column+1]):
			edits = append(edits, applyPatchCandidateEditLine{kind: '-', line: left[row]})
			row++
		default:
			edits = append(edits, applyPatchCandidateEditLine{kind: '+', line: right[column]})
			column++
		}
	}
	for index := len(before) - suffix; index < len(before); index++ {
		edits = append(edits, applyPatchCandidateEditLine{kind: ' ', line: before[index]})
	}
	return edits, ctx.Err()
}

func appendApplyPatchCandidateHunks(
	ctx context.Context,
	builder *applyPatchCandidateBuilder,
	edits []applyPatchCandidateEditLine,
) error {
	changes := make([]int, 0)
	oldPrefix := make([]int, len(edits)+1)
	newPrefix := make([]int, len(edits)+1)
	for index, edit := range edits {
		if index%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		oldPrefix[index+1] = oldPrefix[index]
		newPrefix[index+1] = newPrefix[index]
		if edit.kind != '+' {
			oldPrefix[index+1]++
		}
		if edit.kind != '-' {
			newPrefix[index+1]++
		}
		if edit.kind != ' ' {
			changes = append(changes, index)
		}
	}
	if len(changes) == 0 {
		return builder.append("content unchanged\n")
	}

	groupStart := max(0, changes[0]-applyPatchCandidateContextLines)
	groupEnd := min(len(edits), changes[0]+applyPatchCandidateContextLines+1)
	for index := 1; index <= len(changes); index++ {
		if index < len(changes) {
			nextStart := max(0, changes[index]-applyPatchCandidateContextLines)
			nextEnd := min(len(edits), changes[index]+applyPatchCandidateContextLines+1)
			if nextStart <= groupEnd {
				groupEnd = max(groupEnd, nextEnd)
				continue
			}
		}
		oldStart := oldPrefix[groupStart]
		newStart := newPrefix[groupStart]
		oldCount := oldPrefix[groupEnd] - oldStart
		newCount := newPrefix[groupEnd] - newStart
		if err := builder.append(fmt.Sprintf(
			"@@ -%s +%s @@\n",
			formatApplyPatchCandidateRange(oldStart, oldCount),
			formatApplyPatchCandidateRange(newStart, newCount),
		)); err != nil {
			return err
		}
		for editIndex := groupStart; editIndex < groupEnd; editIndex++ {
			if err := builder.append(
				string(edits[editIndex].kind) + edits[editIndex].line.text + "\n",
			); err != nil {
				return err
			}
			if !edits[editIndex].line.newline {
				if err := builder.append(applyPatchCandidateNoNewlineMarker + "\n"); err != nil {
					return err
				}
			}
		}
		if index < len(changes) {
			groupStart = max(0, changes[index]-applyPatchCandidateContextLines)
			groupEnd = min(len(edits), changes[index]+applyPatchCandidateContextLines+1)
		}
	}
	return ctx.Err()
}

func formatApplyPatchCandidateRange(start, count int) string {
	begin := start + 1
	if count == 0 {
		begin--
	}
	if count == 1 {
		return strconv.Itoa(begin)
	}
	return fmt.Sprintf("%d,%d", begin, count)
}

func applyPatchCandidateFence(content string) string {
	longest := 0
	current := 0
	for _, character := range content {
		if character == '`' {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
