package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	applyPatchBeginMarker     = "*** Begin Patch"
	applyPatchEndMarker       = "*** End Patch"
	applyPatchEndOfFileMarker = "*** End of File"
	applyPatchNoNewlineMarker = `\ No newline at end of file`
)

// ApplyPatchPreflightPolicy adds exact protected filesystem roots and a raw
// caller guard to the built-in workspace and Git-control policy. Inputs are
// detached by NewApplyPatchToolWithPermissionsAndPolicy.
type ApplyPatchPreflightPolicy struct {
	ProtectedRoots []string
	PathGuard      func(string) error
}

type ApplyPatchTool struct {
	workspace      string
	restrict       bool
	allowPaths     []*regexp.Regexp
	allowCreate    bool
	allowUpdate    bool
	pathGuard      func(string) error
	protectedRoots []applyPatchProtectedRoot

	// Deterministic package-test seams. Both run while the canonical workspace
	// gate is held and before the point of no return.
	beforeRevalidate     func(*applyPatchPlan)
	beforeCommit         func(*applyPatchPlan)
	beforeSourceOpen     func(string)
	beforePathFence      func(string)
	afterPointOfNoReturn func(*applyPatchPlan)
}

func NewApplyPatchTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *ApplyPatchTool {
	return NewApplyPatchToolWithPermissions(workspace, restrict, true, true, allowPaths...)
}

// NewApplyPatchToolWithPathGuard creates an apply-patch tool whose caller-owned
// guard participates in the complete preflight before any filesystem mutation.
func NewApplyPatchToolWithPathGuard(
	workspace string,
	restrict bool,
	pathGuard func(string) error,
	allowPaths ...[]*regexp.Regexp,
) *ApplyPatchTool {
	tool := NewApplyPatchToolWithPermissions(
		workspace,
		restrict,
		true,
		true,
		allowPaths...,
	)
	tool.pathGuard = pathGuard
	return tool
}

func NewApplyPatchToolWithPermissions(
	workspace string,
	restrict bool,
	allowCreate bool,
	allowUpdate bool,
	allowPaths ...[]*regexp.Regexp,
) *ApplyPatchTool {
	return &ApplyPatchTool{
		workspace: workspace, restrict: restrict,
		allowCreate: allowCreate, allowUpdate: allowUpdate,
		allowPaths: cloneApplyPatchPatterns(allowPaths),
	}
}

// NewApplyPatchToolWithPermissionsAndPolicy constructs a tool with detached,
// canonical protected-root policy. Invalid roots fail before a tool is exposed.
func NewApplyPatchToolWithPermissionsAndPolicy(
	workspace string,
	restrict bool,
	allowCreate bool,
	allowUpdate bool,
	policy ApplyPatchPreflightPolicy,
	allowPaths ...[]*regexp.Regexp,
) (*ApplyPatchTool, error) {
	protected, err := prepareApplyPatchProtectedRoots(workspace, policy.ProtectedRoots)
	if err != nil {
		return nil, err
	}
	return &ApplyPatchTool{
		workspace:      workspace,
		restrict:       restrict,
		allowPaths:     cloneApplyPatchPatterns(allowPaths),
		allowCreate:    allowCreate,
		allowUpdate:    allowUpdate,
		pathGuard:      policy.PathGuard,
		protectedRoots: protected,
	}, nil
}

func cloneApplyPatchPatterns(allowPaths [][]*regexp.Regexp) []*regexp.Regexp {
	if len(allowPaths) == 0 {
		return nil
	}
	return append([]*regexp.Regexp(nil), allowPaths[0]...)
}

func (t *ApplyPatchTool) Name() string {
	return "apply_patch"
}

func (t *ApplyPatchTool) Description() string {
	return "Apply a Codex-style patch with Begin Patch/End Patch blocks. Supports add, delete, update, and move operations."
}

func (t *ApplyPatchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patch": map[string]any{
				"type":        "string",
				"description": "Patch text beginning with *** Begin Patch and ending with *** End Patch.",
			},
		},
		"required": []string{"patch"},
	}
}

func (t *ApplyPatchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	patch := compatStringArg(args, "patch")
	if strings.TrimSpace(patch) == "" {
		return ErrorResult("patch is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ErrorResult(err.Error())
	}
	ops, err := parseCodexPatchContext(ctx, patch)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if len(ops) == 0 {
		return ErrorResult("patch contains no file operations")
	}

	workspace, unlock, err := globalApplyPatchGates.lock(ctx, t.workspace)
	if err != nil {
		return ErrorResult(err.Error())
	}
	defer unlock()

	plan, err := t.planPatch(ctx, workspace, ops)
	if err != nil {
		return ErrorResult(err.Error())
	}
	candidateResult, err := buildApplyPatchCandidateResult(ctx, plan)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if t.beforeRevalidate != nil {
		t.beforeRevalidate(plan)
	}
	if err := ctx.Err(); err != nil {
		return ErrorResult(err.Error())
	}
	if err := revalidateApplyPatchPlan(ctx, plan); err != nil {
		return ErrorResult(err.Error())
	}
	if t.beforeCommit != nil {
		t.beforeCommit(plan)
	}
	if err := ctx.Err(); err != nil {
		return ErrorResult(err.Error())
	}

	// Point of no return for P010. P011 replaces this legacy sequential
	// mutation loop with staging and rollback. Cancellation after this point
	// cannot intentionally stop between operations and create a partial patch.
	if t.afterPointOfNoReturn != nil {
		t.afterPointOfNoReturn(plan)
	}
	if err := commitApplyPatchPlan(plan); err != nil {
		return ErrorResult(err.Error())
	}
	return candidateResult
}

type codexPatchOp struct {
	kind   string
	path   string
	moveTo string
	hunks  []codexPatchHunk
	add    []byte
}

type codexPatchHunk struct {
	section   string
	lines     []codexPatchLine
	endOfFile bool
}

type codexPatchLine struct {
	kind      byte
	text      string
	newline   bool
	noNewline bool
}

func parseCodexPatchContext(ctx context.Context, patch string) ([]codexPatchOp, error) {
	patch = strings.ReplaceAll(patch, "\r\n", "\n")
	if strings.HasSuffix(patch, "\n") {
		patch = strings.TrimSuffix(patch, "\n")
	}
	lines := strings.Split(patch, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != applyPatchBeginMarker {
		return nil, fmt.Errorf("patch must start with %s", applyPatchBeginMarker)
	}
	if strings.TrimSpace(lines[len(lines)-1]) != applyPatchEndMarker {
		return nil, fmt.Errorf("patch must end with %s", applyPatchEndMarker)
	}

	ops := make([]codexPatchOp, 0)
	for i := 1; i < len(lines)-1; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
			i++
			content := make([]byte, 0)
			for i < len(lines)-1 && !isCodexPatchOperationLine(lines[i]) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("add file %q line must start with +", path)
				}
				var appendErr error
				content, appendErr = appendApplyPatchTextContext(ctx, content, lines[i][1:], true)
				if appendErr != nil {
					return nil, appendErr
				}
				i++
			}
			ops = append(ops, codexPatchOp{kind: "add", path: path, add: content})
		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			ops = append(ops, codexPatchOp{kind: "delete", path: path})
			i++
		case strings.HasPrefix(line, "*** Update File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			op := codexPatchOp{kind: "update", path: path}
			moveSeen := false
			i++
			for i < len(lines)-1 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				current := lines[i]
				switch {
				case strings.HasPrefix(current, "*** Move to: "):
					if moveSeen {
						return nil, fmt.Errorf("update file %q has an invalid move directive", path)
					}
					moveSeen = true
					op.moveTo = strings.TrimSpace(strings.TrimPrefix(current, "*** Move to: "))
					if op.moveTo == "" {
						return nil, fmt.Errorf("update file %q has a blank move destination", path)
					}
					i++
				case isCodexPatchOperationLine(current):
					goto updateDone
				case strings.HasPrefix(current, "@@"):
					hunk, next, hunkErr := parseCodexPatchHunk(
						ctx,
						lines,
						i+1,
						strings.TrimSpace(strings.TrimPrefix(current, "@@")),
					)
					if hunkErr != nil {
						return nil, fmt.Errorf("update file %q: %w", path, hunkErr)
					}
					op.hunks = append(op.hunks, hunk)
					i = next
				default:
					return nil, fmt.Errorf("update file %q expected hunk header or move directive", path)
				}
			}
		updateDone:
			if len(op.hunks) == 0 && op.moveTo == "" {
				return nil, fmt.Errorf("update file %q contains no hunks or move", path)
			}
			ops = append(ops, op)
		case line == "":
			i++
		default:
			return nil, fmt.Errorf("unexpected patch line: %s", line)
		}
	}
	return ops, nil
}

func isCodexPatchOperationLine(line string) bool {
	return strings.HasPrefix(line, "*** Add File: ") ||
		strings.HasPrefix(line, "*** Delete File: ") ||
		strings.HasPrefix(line, "*** Update File: ")
}

func parseCodexPatchHunk(
	ctx context.Context,
	lines []string,
	start int,
	section string,
) (codexPatchHunk, int, error) {
	hunk := codexPatchHunk{section: section}
	for i := start; i < len(lines)-1; i++ {
		if err := ctx.Err(); err != nil {
			return codexPatchHunk{}, i, err
		}
		line := lines[i]
		if strings.HasPrefix(line, "@@") || isCodexPatchOperationLine(line) ||
			strings.HasPrefix(line, "*** Move to: ") {
			if len(hunk.lines) == 0 {
				return codexPatchHunk{}, i, fmt.Errorf("hunk is empty")
			}
			return hunk, i, validateCodexPatchHunk(ctx, hunk)
		}
		if line == applyPatchEndOfFileMarker {
			if len(hunk.lines) == 0 || hunk.endOfFile {
				return codexPatchHunk{}, i, fmt.Errorf("end-of-file marker is misplaced or duplicated")
			}
			hunk.endOfFile = true
			next := i + 1
			if next < len(lines)-1 && !isCodexPatchOperationLine(lines[next]) {
				return codexPatchHunk{}, next, fmt.Errorf("end-of-file marker must terminate the hunk")
			}
			return hunk, next, validateCodexPatchHunk(ctx, hunk)
		}
		if line == applyPatchNoNewlineMarker {
			if len(hunk.lines) == 0 || hunk.lines[len(hunk.lines)-1].noNewline {
				return codexPatchHunk{}, i, fmt.Errorf("no-newline marker is misplaced or duplicated")
			}
			hunk.lines[len(hunk.lines)-1].newline = false
			hunk.lines[len(hunk.lines)-1].noNewline = true
			continue
		}

		parsed := codexPatchLine{newline: true}
		if line == "" {
			parsed.kind = ' '
		} else {
			parsed.kind = line[0]
			parsed.text = line[1:]
			if parsed.kind != ' ' && parsed.kind != '-' && parsed.kind != '+' {
				return codexPatchHunk{}, i, fmt.Errorf("hunk line must start with space, -, or +")
			}
		}
		hunk.lines = append(hunk.lines, parsed)
	}
	if len(hunk.lines) == 0 {
		return codexPatchHunk{}, len(lines) - 1, fmt.Errorf("hunk is empty")
	}
	return hunk, len(lines) - 1, validateCodexPatchHunk(ctx, hunk)
}

func validateCodexPatchHunk(ctx context.Context, hunk codexPatchHunk) error {
	oldTerminal := false
	newTerminal := false
	hasChange := false
	for _, line := range hunk.lines {
		if line.kind == '-' || line.kind == '+' {
			hasChange = true
		}
		if oldTerminal && (line.kind == ' ' || line.kind == '-') {
			return fmt.Errorf("old side continues after no-newline marker")
		}
		if newTerminal && (line.kind == ' ' || line.kind == '+') {
			return fmt.Errorf("new side continues after no-newline marker")
		}
		if line.noNewline {
			if line.kind == ' ' || line.kind == '-' {
				oldTerminal = true
			}
			if line.kind == ' ' || line.kind == '+' {
				newTerminal = true
			}
		}
	}
	if !hasChange {
		return fmt.Errorf("hunk contains no changes")
	}
	oldBytes, _, err := codexPatchHunkBytesContext(ctx, hunk)
	if err != nil {
		return err
	}
	if len(oldBytes) == 0 && !hunk.endOfFile {
		return fmt.Errorf("pure insertion requires %s", applyPatchEndOfFileMarker)
	}
	return nil
}

func codexPatchHunkBytesContext(
	ctx context.Context,
	hunk codexPatchHunk,
) ([]byte, []byte, error) {
	oldText := make([]byte, 0)
	newText := make([]byte, 0)
	for _, line := range hunk.lines {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		switch line.kind {
		case ' ':
			var err error
			oldText, err = appendApplyPatchTextContext(ctx, oldText, line.text, line.newline)
			if err != nil {
				return nil, nil, err
			}
			newText, err = appendApplyPatchTextContext(ctx, newText, line.text, line.newline)
			if err != nil {
				return nil, nil, err
			}
		case '-':
			var err error
			oldText, err = appendApplyPatchTextContext(ctx, oldText, line.text, line.newline)
			if err != nil {
				return nil, nil, err
			}
		case '+':
			var err error
			newText, err = appendApplyPatchTextContext(ctx, newText, line.text, line.newline)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return oldText, newText, nil
}

func appendApplyPatchTextContext(
	ctx context.Context,
	destination []byte,
	text string,
	newline bool,
) ([]byte, error) {
	const chunkSize = 64 * 1024
	for start := 0; start < len(text); start += chunkSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+chunkSize, len(text))
		destination = append(destination, text[start:end]...)
	}
	if newline {
		destination = append(destination, '\n')
	}
	return destination, ctx.Err()
}

func (t *ApplyPatchTool) resolveWritePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("patch file path is required")
	}
	return validatePathWithAllowPaths(path, t.workspace, t.restrict, t.allowPaths)
}

func applyPatchParentMode() os.FileMode { return 0o755 }
func applyPatchFileMode() os.FileMode   { return 0o644 }
