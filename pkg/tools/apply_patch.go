package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ApplyPatchTool struct {
	workspace   string
	restrict    bool
	allowPaths  []*regexp.Regexp
	allowCreate bool
	allowUpdate bool
	pathGuard   func(string) error
}

func NewApplyPatchTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *ApplyPatchTool {
	return NewApplyPatchToolWithPermissions(workspace, restrict, true, true, allowPaths...)
}

// NewApplyPatchToolWithPathGuard creates an apply-patch tool whose caller-owned
// guard validates every source and move destination after the complete patch
// parses and before the first operation mutates the filesystem. It is intended
// for narrow controller runtimes that impose a stricter boundary than the
// ordinary workspace restriction (for example, denying Git control paths).
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
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &ApplyPatchTool{
		workspace:   workspace,
		restrict:    restrict,
		allowPaths:  patterns,
		allowCreate: allowCreate,
		allowUpdate: allowUpdate,
	}
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

func (t *ApplyPatchTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	patch := strings.TrimSpace(compatStringArg(args, "patch"))
	if patch == "" {
		return ErrorResult("patch is required")
	}
	ops, err := parseCodexPatch(patch)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if len(ops) == 0 {
		return ErrorResult("patch contains no file operations")
	}
	if t.pathGuard != nil {
		for _, op := range ops {
			if err := t.pathGuard(op.path); err != nil {
				return ErrorResult(fmt.Sprintf("patch path %q is denied: %v", op.path, err))
			}
			if op.moveTo != "" {
				if err := t.pathGuard(op.moveTo); err != nil {
					return ErrorResult(fmt.Sprintf(
						"patch move path %q is denied: %v",
						op.moveTo,
						err,
					))
				}
			}
		}
	}

	var summaries []string
	for _, op := range ops {
		summary, err := t.applyOp(op)
		if err != nil {
			return ErrorResult(err.Error())
		}
		summaries = append(summaries, summary)
	}
	return NewToolResult(strings.Join(summaries, "\n"))
}

type codexPatchOp struct {
	kind   string
	path   string
	moveTo string
	hunks  []codexPatchHunk
	add    string
}

type codexPatchHunk struct {
	oldText string
	newText string
}

func parseCodexPatch(patch string) ([]codexPatchOp, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with *** Begin Patch")
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "*** End Patch" {
		return nil, fmt.Errorf("patch must end with *** End Patch")
	}

	var ops []codexPatchOp
	for i := 1; i < len(lines)-1; {
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
			i++
			var content strings.Builder
			for i < len(lines)-1 && !strings.HasPrefix(strings.TrimSpace(lines[i]), "*** ") {
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("add file %q line must start with +", path)
				}
				content.WriteString(strings.TrimPrefix(lines[i], "+"))
				content.WriteByte('\n')
				i++
			}
			ops = append(ops, codexPatchOp{kind: "add", path: path, add: content.String()})
		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			ops = append(ops, codexPatchOp{kind: "delete", path: path})
			i++
		case strings.HasPrefix(line, "*** Update File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			op := codexPatchOp{kind: "update", path: path}
			i++
			for i < len(lines)-1 {
				trimmed := strings.TrimSpace(lines[i])
				if strings.HasPrefix(trimmed, "*** Move to: ") {
					op.moveTo = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Move to: "))
					i++
					continue
				}
				if strings.HasPrefix(trimmed, "*** ") {
					break
				}
				if strings.HasPrefix(trimmed, "@@") {
					i++
					hunk, next, err := parseCodexPatchHunk(lines, i)
					if err != nil {
						return nil, fmt.Errorf("update file %q: %w", path, err)
					}
					op.hunks = append(op.hunks, hunk)
					i = next
					continue
				}
				return nil, fmt.Errorf("update file %q expected hunk header or move directive", path)
			}
			ops = append(ops, op)
		case line == "":
			i++
		default:
			return nil, fmt.Errorf("unexpected patch line: %s", lines[i])
		}
	}
	return ops, nil
}

func parseCodexPatchHunk(lines []string, start int) (codexPatchHunk, int, error) {
	var oldText strings.Builder
	var newText strings.Builder
	i := start
	for i < len(lines)-1 {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "*** ") || strings.HasPrefix(trimmed, "@@") {
			break
		}
		if line == `\ No newline at end of file` {
			i++
			continue
		}
		if line == "" {
			oldText.WriteByte('\n')
			newText.WriteByte('\n')
			i++
			continue
		}
		prefix := line[0]
		text := line[1:] + "\n"
		switch prefix {
		case ' ':
			oldText.WriteString(text)
			newText.WriteString(text)
		case '-':
			oldText.WriteString(text)
		case '+':
			newText.WriteString(text)
		default:
			return codexPatchHunk{}, i, fmt.Errorf("hunk line must start with space, -, or +")
		}
		i++
	}
	return codexPatchHunk{oldText: oldText.String(), newText: newText.String()}, i, nil
}

func (t *ApplyPatchTool) applyOp(op codexPatchOp) (string, error) {
	switch op.kind {
	case "add":
		if !t.allowCreate {
			return "", fmt.Errorf("add file %q failed: write_file is disabled", op.path)
		}
		path, err := t.resolveWritePath(op.path)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("add file %q failed: file already exists", op.path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("add file %q failed: %w", op.path, err)
		}
		if err := os.WriteFile(path, []byte(op.add), 0o644); err != nil {
			return "", fmt.Errorf("add file %q failed: %w", op.path, err)
		}
		return fmt.Sprintf("added %s", op.path), nil
	case "delete":
		if !t.allowCreate {
			return "", fmt.Errorf("delete file %q failed: write_file is disabled", op.path)
		}
		path, err := t.resolveWritePath(op.path)
		if err != nil {
			return "", err
		}
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("delete file %q failed: %w", op.path, err)
		}
		return fmt.Sprintf("deleted %s", op.path), nil
	case "update":
		return t.applyUpdate(op)
	default:
		return "", fmt.Errorf("unsupported patch operation %q", op.kind)
	}
}

func (t *ApplyPatchTool) applyUpdate(op codexPatchOp) (string, error) {
	if !t.allowUpdate {
		return "", fmt.Errorf("update file %q failed: edit_file is disabled", op.path)
	}
	path, err := t.resolveWritePath(op.path)
	if err != nil {
		return "", err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("update file %q failed: %w", op.path, err)
	}
	updated := string(before)
	for _, hunk := range op.hunks {
		if hunk.oldText == "" && hunk.newText == "" {
			continue
		}
		if !strings.Contains(updated, hunk.oldText) {
			return "", fmt.Errorf("update file %q failed: hunk context not found", op.path)
		}
		updated = strings.Replace(updated, hunk.oldText, hunk.newText, 1)
	}

	target := path
	targetLabel := op.path
	if strings.TrimSpace(op.moveTo) != "" {
		if !t.allowCreate {
			return "", fmt.Errorf("move file %q failed: write_file is disabled", op.path)
		}
		target, err = t.resolveWritePath(op.moveTo)
		if err != nil {
			return "", err
		}
		targetLabel = op.moveTo
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", fmt.Errorf("move file %q failed: %w", op.moveTo, err)
		}
	}
	if err := os.WriteFile(target, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("update file %q failed: %w", targetLabel, err)
	}
	if target != path {
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("move file %q failed after writing target: %w", op.path, err)
		}
		return fmt.Sprintf("moved %s to %s", op.path, op.moveTo), nil
	}
	return fmt.Sprintf("updated %s", op.path), nil
}

func (t *ApplyPatchTool) resolveWritePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("patch file path is required")
	}
	return validatePathWithAllowPaths(path, t.workspace, t.restrict, t.allowPaths)
}
