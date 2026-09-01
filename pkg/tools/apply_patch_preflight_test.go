package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

type applyPatchTreeEntry struct {
	Mode       os.FileMode
	Content    string
	LinkTarget string
}

type applyPatchCancelAfterChecksContext struct {
	context.Context
	remaining int
}

func (ctx *applyPatchCancelAfterChecksContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func applyPatchSnapshotTree(t *testing.T, root string) map[string]applyPatchTreeEntry {
	t.Helper()
	snapshot := make(map[string]applyPatchTreeEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		item := applyPatchTreeEntry{Mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.LinkTarget, err = os.Readlink(path)
		case info.Mode().IsRegular():
			var content []byte
			content, err = os.ReadFile(path)
			item.Content = string(content)
		}
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(rel)] = item
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot tree %s: %v", root, err)
	}
	return snapshot
}

func assertApplyPatchTreeEqual(
	t *testing.T,
	root string,
	want map[string]applyPatchTreeEntry,
) {
	t.Helper()
	got := applyPatchSnapshotTree(t, root)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tree changed\n got: %#v\nwant: %#v", got, want)
	}
}

func writeApplyPatchFixture(
	t *testing.T,
	root string,
	rel string,
	content string,
	mode os.FileMode,
) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod %s: %v", rel, err)
		}
	}
	return path
}

func readApplyPatchFixture(t *testing.T, root, rel string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(content)
}

func newApplyPatchPreflightTestTool(
	t *testing.T,
	workspace string,
	allowCreate bool,
	allowUpdate bool,
	policy ApplyPatchPreflightPolicy,
	allowPaths ...[]*regexp.Regexp,
) *ApplyPatchTool {
	t.Helper()
	if policy.TransactionStateRoot == "" {
		policy.TransactionStateRoot = filepath.Join(
			t.TempDir(),
			"apply-patch-transactions",
		)
	}
	tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		true,
		allowCreate,
		allowUpdate,
		policy,
		allowPaths...,
	)
	if err != nil {
		t.Fatalf("NewApplyPatchToolWithPermissionsAndPolicy() error = %v", err)
	}
	return tool
}

func executeApplyPatch(t *testing.T, tool Tool, ctx context.Context, patch string) *ToolResult {
	t.Helper()
	result := tool.Execute(ctx, map[string]any{"patch": patch})
	if result == nil {
		t.Fatal("apply_patch returned nil")
	}
	return result
}

func requireApplyPatchError(t *testing.T, result *ToolResult, contains ...string) {
	t.Helper()
	if result == nil || !result.IsError {
		t.Fatalf("result = %#v, want error", result)
	}
	for _, text := range contains {
		if !strings.Contains(strings.ToLower(result.ForLLM), strings.ToLower(text)) {
			t.Fatalf("error = %q, want substring %q", result.ForLLM, text)
		}
	}
}

func TestApplyPatchPreflightLaterInvalidLeavesExactTree(t *testing.T) {
	tests := []struct {
		name        string
		allowCreate bool
		allowUpdate bool
		setup       func(*testing.T, string)
		patch       func(string) string
	}{
		{
			name: "missing later update source", allowCreate: true, allowUpdate: true,
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: first.txt\n@@\n-before\n+after\n" +
					"*** Update File: missing.txt\n@@\n-old\n+new\n" +
					"*** End Patch"
			},
		},
		{
			name: "missing later delete source", allowCreate: true, allowUpdate: true,
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: first.txt\n@@\n-before\n+after\n" +
					"*** Delete File: missing.txt\n" +
					"*** End Patch"
			},
		},
		{
			name: "existing later add target", allowCreate: true, allowUpdate: true,
			setup: func(t *testing.T, root string) {
				writeApplyPatchFixture(t, root, "existing.txt", "keep\n", 0o640)
			},
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: first.txt\n@@\n-before\n+after\n" +
					"*** Add File: existing.txt\n+replace\n" +
					"*** End Patch"
			},
		},
		{
			name: "later unmatched hunk", allowCreate: true, allowUpdate: true,
			setup: func(t *testing.T, root string) {
				writeApplyPatchFixture(t, root, "second.txt", "present\n", 0o600)
			},
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: first.txt\n@@\n-before\n+after\n" +
					"*** Update File: second.txt\n@@\n-absent\n+new\n" +
					"*** End Patch"
			},
		},
		{
			name: "later ambiguous hunk", allowCreate: true, allowUpdate: true,
			setup: func(t *testing.T, root string) {
				writeApplyPatchFixture(t, root, "second.txt", "repeat\nrepeat\n", 0o600)
			},
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: first.txt\n@@\n-before\n+after\n" +
					"*** Update File: second.txt\n@@\n-repeat\n+new\n" +
					"*** End Patch"
			},
		},
		{
			name: "later target below regular file", allowCreate: true, allowUpdate: true,
			setup: func(t *testing.T, root string) {
				writeApplyPatchFixture(t, root, "not-a-directory", "parent\n", 0o600)
			},
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: first.txt\n@@\n-before\n+after\n" +
					"*** Add File: not-a-directory/child/grandchild.txt\n+new\n" +
					"*** End Patch"
			},
		},
		{
			name: "later create permission denial", allowCreate: false, allowUpdate: true,
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: first.txt\n@@\n-before\n+after\n" +
					"*** Add File: nested/new.txt\n+new\n" +
					"*** End Patch"
			},
		},
		{
			name: "later update permission denial", allowCreate: true, allowUpdate: false,
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Add File: nested/new.txt\n+new\n" +
					"*** Update File: first.txt\n@@\n-before\n+after\n" +
					"*** End Patch"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "first.txt", "before\n", 0o751)
			if test.setup != nil {
				test.setup(t, workspace)
			}
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, test.allowCreate, test.allowUpdate, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch(workspace))
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightAllPublicConstructorsUseWholePlan(t *testing.T) {
	tests := []struct {
		name  string
		build func(*testing.T, string) *ApplyPatchTool
	}{
		{
			name: "ordinary",
			build: func(_ *testing.T, workspace string) *ApplyPatchTool {
				return NewApplyPatchTool(workspace, true)
			},
		},
		{
			name: "permissions",
			build: func(_ *testing.T, workspace string) *ApplyPatchTool {
				return NewApplyPatchToolWithPermissions(workspace, true, true, true)
			},
		},
		{
			name: "path guard",
			build: func(_ *testing.T, workspace string) *ApplyPatchTool {
				return NewApplyPatchToolWithPathGuard(workspace, true, func(string) error { return nil })
			},
		},
		{
			name: "policy",
			build: func(t *testing.T, workspace string) *ApplyPatchTool {
				tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
					workspace, true, true, true, ApplyPatchPreflightPolicy{},
				)
				if err != nil {
					t.Fatalf("NewApplyPatchToolWithPermissionsAndPolicy() error = %v", err)
				}
				return tool
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			isolateApplyPatchDefaultTransactionState(t)
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "first.txt", "before\n", 0o751)
			before := applyPatchSnapshotTree(t, workspace)
			result := executeApplyPatch(t, test.build(t, workspace), context.Background(),
				"*** Begin Patch\n"+
					"*** Update File: first.txt\n@@\n-before\n+after\n"+
					"*** Update File: missing.txt\n@@\n-old\n+new\n"+
					"*** End Patch",
			)
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightLegacyConstructorsDenyGitControlPaths(t *testing.T) {
	tests := []struct {
		name  string
		build func(string) *ApplyPatchTool
	}{
		{name: "ordinary", build: func(workspace string) *ApplyPatchTool {
			return NewApplyPatchTool(workspace, true)
		}},
		{name: "permissions", build: func(workspace string) *ApplyPatchTool {
			return NewApplyPatchToolWithPermissions(workspace, true, true, true)
		}},
		{name: "path guard", build: func(workspace string) *ApplyPatchTool {
			return NewApplyPatchToolWithPathGuard(workspace, true, func(string) error { return nil })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			isolateApplyPatchDefaultTransactionState(t)
			workspace := t.TempDir()
			before := applyPatchSnapshotTree(t, workspace)
			result := executeApplyPatch(t, test.build(workspace), context.Background(),
				"*** Begin Patch\n*** Add File: .git/config\n+unsafe\n*** End Patch",
			)
			requireApplyPatchError(t, result, "git")
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightCancellationLeavesExactTree(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ApplyPatchTool, context.CancelFunc)
		cancelNow bool
	}{
		{name: "pre-canceled", cancelNow: true},
		{
			name: "before revalidate",
			configure: func(tool *ApplyPatchTool, cancel context.CancelFunc) {
				tool.beforeRevalidate = func(*applyPatchPlan) { cancel() }
			},
		},
		{
			name: "before commit",
			configure: func(tool *ApplyPatchTool, cancel context.CancelFunc) {
				tool.beforeCommit = func(*applyPatchPlan) { cancel() }
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "one.txt", "before\n", 0o751)
			before := applyPatchSnapshotTree(t, workspace)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			if test.configure != nil {
				test.configure(tool, cancel)
			}
			if test.cancelNow {
				cancel()
			}
			result := executeApplyPatch(t, tool, ctx,
				"*** Begin Patch\n*** Update File: one.txt\n@@\n-before\n+after\n*** End Patch",
			)
			requireApplyPatchError(t, result, "cancel")
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightCancellationDuringLateGuardLeavesExactTree(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "one.txt", "before\n", 0o751)
	before := applyPatchSnapshotTree(t, workspace)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var guarded []string
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		PathGuard: func(path string) error {
			guarded = append(guarded, path)
			if path == "two.txt" {
				cancel()
			}
			return nil
		},
	})
	result := executeApplyPatch(t, tool, ctx,
		"*** Begin Patch\n"+
			"*** Update File: one.txt\n@@\n-before\n+after\n"+
			"*** Add File: two.txt\n+two\n"+
			"*** End Patch",
	)
	requireApplyPatchError(t, result, "cancel")
	if !reflect.DeepEqual(guarded, []string{"one.txt", "two.txt"}) {
		t.Fatalf("guarded paths = %#v", guarded)
	}
	assertApplyPatchTreeEqual(t, workspace, before)
}

func TestApplyPatchCancellationAfterPointOfNoReturnDoesNotInterruptCommit(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "one.txt", "before\n", 0o751)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	tool.afterPointOfNoReturn = func(*applyPatchPlan) { cancel() }
	result := executeApplyPatch(t, tool, ctx,
		"*** Begin Patch\n"+
			"*** Update File: one.txt\n@@\n-before\n+after\n"+
			"*** Add File: two.txt\n+two\n*** End Patch",
	)
	if result.IsError {
		t.Fatalf("post-PONR cancellation interrupted commit: %s", result.ForLLM)
	}
	if got := readApplyPatchFixture(t, workspace, "one.txt"); got != "after\n" {
		t.Fatalf("updated content = %q", got)
	}
	if got := readApplyPatchFixture(t, workspace, "two.txt"); got != "two\n" {
		t.Fatalf("added content = %q", got)
	}
}

func TestApplyPatchPreflightPathGuardSeesEachAuthoredRoleOnce(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "source\n", 0o751)
	var guarded []string
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		PathGuard: func(path string) error {
			guarded = append(guarded, path)
			return nil
		},
	})
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Update File: source.txt\n*** Move to: moved.txt\n"+
			"*** Add File: added.txt\n+added\n"+
			"*** End Patch",
	)
	if result.IsError {
		t.Fatalf("patch failed: %s", result.ForLLM)
	}
	if !reflect.DeepEqual(guarded, []string{"source.txt", "moved.txt", "added.txt"}) {
		t.Fatalf("guarded paths = %#v", guarded)
	}
}

func TestApplyPatchPreflightPortableGitAliasesAreMutationFree(t *testing.T) {
	aliases := []string{
		".git/config",
		".GIT/config",
		".git./config",
		".git /config",
		".git:stream/config",
		"nested/.GiT/config",
		`nested\.git\config`,
	}
	for _, alias := range aliases {
		t.Run(strings.ReplaceAll(alias, "/", "_"), func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "first.txt", "before\n", 0o640)
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			patch := fmt.Sprintf(
				"*** Begin Patch\n*** Update File: first.txt\n@@\n-before\n+after\n"+
					"*** Add File: %s\n+forbidden\n*** End Patch",
				alias,
			)
			result := executeApplyPatch(t, tool, context.Background(), patch)
			requireApplyPatchError(t, result, "git")
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightInvalidLatePathsAreMutationFree(t *testing.T) {
	invalidUTF8 := string([]byte{'b', 'a', 'd', 0xff})
	tests := []struct {
		name string
		path func(*testing.T) string
	}{
		{name: "blank", path: func(*testing.T) string { return "   " }},
		{name: "NUL", path: func(*testing.T) string { return "bad\x00path" }},
		{name: "invalid UTF-8", path: func(*testing.T) string { return invalidUTF8 }},
		{name: "outside workspace", path: func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "outside.txt")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "first.txt", "before\n", 0o751)
			before := applyPatchSnapshotTree(t, workspace)
			path := test.path(t)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), fmt.Sprintf(
				"*** Begin Patch\n"+
					"*** Update File: first.txt\n@@\n-before\n+after\n"+
					"*** Add File: %s\n+bad\n"+
					"*** End Patch",
				path,
			))
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
			if filepath.IsAbs(path) {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("outside path exists: %v", err)
				}
			}
		})
	}
}

func TestApplyPatchPreflightResolvedGitAliasIsMutationFree(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, ".git/config", "safe\n", 0o600)
	writeApplyPatchFixture(t, workspace, "ordinary.txt", "before\n", 0o640)
	if err := os.Symlink(".git", filepath.Join(workspace, "control-alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := applyPatchSnapshotTree(t, workspace)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Update File: ordinary.txt\n@@\n-before\n+after\n"+
			"*** Update File: control-alias/config\n@@\n-safe\n+unsafe\n"+
			"*** End Patch",
	)
	requireApplyPatchError(t, result, "git")
	assertApplyPatchTreeEqual(t, workspace, before)
}

func TestApplyPatchPreflightProtectedRootsAreResolvedAndRedacted(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "protected-canary")
	writeApplyPatchFixture(t, protected, "secret.txt", "secret\n", 0o600)
	writeApplyPatchFixture(t, workspace, "ordinary.txt", "before\n", 0o640)
	alias := filepath.Join(workspace, "innocent-alias")
	if err := os.Symlink(protected, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := applyPatchSnapshotTree(t, workspace)
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		ProtectedRoots: []string{protected},
	})
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Update File: ordinary.txt\n@@\n-before\n+after\n"+
			"*** Update File: innocent-alias/secret.txt\n@@\n-secret\n+leaked\n"+
			"*** End Patch",
	)
	requireApplyPatchError(t, result)
	if strings.Contains(result.ForLLM, protected) {
		t.Fatalf("protected canonical root leaked in error: %q", result.ForLLM)
	}
	assertApplyPatchTreeEqual(t, workspace, before)
}

func TestApplyPatchPreflightProtectedFilesChildrenAndMoveTargets(t *testing.T) {
	tests := []struct {
		name          string
		protectedRoot func(string) string
		patch         string
	}{
		{
			name:          "exact protected file",
			protectedRoot: func(root string) string { return filepath.Join(root, "control.json") },
			patch: "*** Begin Patch\n" +
				"*** Update File: ordinary.txt\n@@\n-before\n+after\n" +
				"*** Update File: control.json\n@@\n-control\n+changed\n" +
				"*** End Patch",
		},
		{
			name:          "missing child of protected directory",
			protectedRoot: func(root string) string { return filepath.Join(root, "control") },
			patch: "*** Begin Patch\n" +
				"*** Update File: ordinary.txt\n@@\n-before\n+after\n" +
				"*** Add File: control/new/child.txt\n+changed\n" +
				"*** End Patch",
		},
		{
			name:          "missing protected root and child",
			protectedRoot: func(root string) string { return filepath.Join(root, "future-control") },
			patch: "*** Begin Patch\n" +
				"*** Update File: ordinary.txt\n@@\n-before\n+after\n" +
				"*** Add File: future-control/new/child.txt\n+changed\n" +
				"*** End Patch",
		},
		{
			name:          "move destination in protected directory",
			protectedRoot: func(root string) string { return filepath.Join(root, "control") },
			patch: "*** Begin Patch\n" +
				"*** Update File: source.txt\n*** Move to: control/moved.txt\n" +
				"*** End Patch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "ordinary.txt", "before\n", 0o640)
			writeApplyPatchFixture(t, workspace, "source.txt", "source\n", 0o751)
			writeApplyPatchFixture(t, workspace, "control.json", "control\n", 0o600)
			if err := os.MkdirAll(filepath.Join(workspace, "control"), 0o700); err != nil {
				t.Fatal(err)
			}
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
				ProtectedRoots: []string{test.protectedRoot(workspace)},
			})
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightPolicyRootsAreDetached(t *testing.T) {
	workspace := t.TempDir()
	protectedA := filepath.Join(workspace, "protected-a")
	protectedB := filepath.Join(workspace, "protected-b")
	writeApplyPatchFixture(t, protectedA, "secret.txt", "a\n", 0o600)
	writeApplyPatchFixture(t, protectedB, "secret.txt", "b\n", 0o600)
	roots := []string{protectedA}
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		ProtectedRoots: roots,
	})
	roots[0] = protectedB
	before := applyPatchSnapshotTree(t, workspace)
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Update File: protected-a/secret.txt\n@@\n-a\n+changed\n*** End Patch",
	)
	requireApplyPatchError(t, result)
	assertApplyPatchTreeEqual(t, workspace, before)
}

func TestApplyPatchPreflightPolicyRejectsConstructionTimeRootDrift(t *testing.T) {
	workspace := t.TempDir()
	firstProtected := filepath.Join(workspace, "protected-first")
	secondProtected := filepath.Join(workspace, "protected-second")
	if err := os.Mkdir(firstProtected, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(secondProtected, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(workspace, "protected-alias")
	if err := os.Symlink("protected-first", alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		ProtectedRoots: []string{alias},
	})
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("protected-second", alias); err != nil {
		t.Fatal(err)
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Add File: ordinary.txt\n+blocked-on-policy-drift\n*** End Patch",
	)
	requireApplyPatchError(t, result, "protected root")
	if _, err := os.Lstat(filepath.Join(workspace, "ordinary.txt")); !os.IsNotExist(err) {
		t.Fatalf("policy drift allowed mutation: %v", err)
	}
}

func TestApplyPatchPreflightPolicyRejectsInvalidProtectedRootAtConstruction(t *testing.T) {
	tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
		t.TempDir(),
		true,
		true,
		true,
		ApplyPatchPreflightPolicy{ProtectedRoots: []string{"invalid\x00root"}},
	)
	if err == nil || tool != nil {
		t.Fatalf("constructor = %#v, %v; want nil tool and non-nil error", tool, err)
	}
}

func TestApplyPatchPreflightRejectsTerminalAndDanglingSymlinks(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, string, string)
		patch  string
		verify func(*testing.T, string, string)
	}{
		{
			name: "terminal source symlink",
			setup: func(t *testing.T, workspace, _ string) {
				writeApplyPatchFixture(t, workspace, "real.txt", "before\n", 0o640)
				if err := os.Symlink("real.txt", filepath.Join(workspace, "link.txt")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			patch: "*** Begin Patch\n*** Update File: link.txt\n@@\n-before\n+after\n*** End Patch",
		},
		{
			name: "dangling internal add target",
			setup: func(t *testing.T, workspace, _ string) {
				if err := os.Symlink("missing.txt", filepath.Join(workspace, "link.txt")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			patch: "*** Begin Patch\n*** Add File: link.txt\n+created\n*** End Patch",
		},
		{
			name: "dangling outside add target",
			setup: func(t *testing.T, workspace, outside string) {
				if err := os.Symlink(
					filepath.Join(outside, "canary.txt"),
					filepath.Join(workspace, "link.txt"),
				); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			patch: "*** Begin Patch\n*** Add File: link.txt\n+created\n*** End Patch",
			verify: func(t *testing.T, _, outside string) {
				if _, err := os.Lstat(filepath.Join(outside, "canary.txt")); !os.IsNotExist(err) {
					t.Fatalf("outside canary was created: %v", err)
				}
			},
		},
		{
			name: "dangling outside move target",
			setup: func(t *testing.T, workspace, outside string) {
				writeApplyPatchFixture(t, workspace, "source.txt", "source\n", 0o751)
				if err := os.Symlink(
					filepath.Join(outside, "canary.txt"),
					filepath.Join(workspace, "link.txt"),
				); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			patch: "*** Begin Patch\n*** Update File: source.txt\n*** Move to: link.txt\n*** End Patch",
			verify: func(t *testing.T, _, outside string) {
				if _, err := os.Lstat(filepath.Join(outside, "canary.txt")); !os.IsNotExist(err) {
					t.Fatalf("outside canary was created: %v", err)
				}
			},
		},
		{
			name: "existing terminal move symlink",
			setup: func(t *testing.T, workspace, outside string) {
				writeApplyPatchFixture(t, workspace, "source.txt", "source\n", 0o751)
				writeApplyPatchFixture(t, outside, "canary.txt", "outside\n", 0o600)
				if err := os.Symlink(
					filepath.Join(outside, "canary.txt"),
					filepath.Join(workspace, "link.txt"),
				); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			patch: "*** Begin Patch\n*** Update File: source.txt\n*** Move to: link.txt\n*** End Patch",
			verify: func(t *testing.T, _, outside string) {
				if got := readApplyPatchFixture(t, outside, "canary.txt"); got != "outside\n" {
					t.Fatalf("outside canary changed to %q", got)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			outside := t.TempDir()
			test.setup(t, workspace, outside)
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			requireApplyPatchError(t, result, "symlink")
			assertApplyPatchTreeEqual(t, workspace, before)
			if test.verify != nil {
				test.verify(t, workspace, outside)
			}
		})
	}
}

func TestApplyPatchPreflightSourceOpenDoesNotFollowLeafSwap(t *testing.T) {
	workspace := t.TempDir()
	source := writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o751)
	outside := writeApplyPatchFixture(t, t.TempDir(), "outside.txt", "outside\n", 0o600)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	tool.beforeSourceOpen = func(string) {
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, source); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
	)
	requireApplyPatchError(t, result)
	if got := readApplyPatchFixture(t, filepath.Dir(outside), filepath.Base(outside)); got != "outside\n" {
		t.Fatalf("outside source changed through leaf swap: %q", got)
	}
	if target, err := os.Readlink(source); err != nil || target != outside {
		t.Fatalf("external leaf swap = %q, %v", target, err)
	}
}

func TestApplyPatchPreflightRejectsDanglingIntermediateSymlinkBeforeMutation(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "ordinary.txt", "before\n", 0o751)
	if err := os.Symlink("missing-directory", filepath.Join(workspace, "dangling")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := applyPatchSnapshotTree(t, workspace)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Update File: ordinary.txt\n@@\n-before\n+after\n"+
			"*** Add File: dangling/child.txt\n+forbidden\n"+
			"*** End Patch",
	)
	requireApplyPatchError(t, result, "symlink")
	assertApplyPatchTreeEqual(t, workspace, before)
}

func TestApplyPatchPreflightAllowsSafeIntermediateSymlink(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(workspace, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeApplyPatchFixture(t, workspace, "real/existing.txt", "before\n", 0o751)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Update File: alias/existing.txt\n@@\n-before\n+after\n"+
			"*** Add File: alias/new.txt\n+new\n"+
			"*** End Patch",
	)
	if result.IsError {
		t.Fatalf("safe intermediate symlink failed: %s", result.ForLLM)
	}
	if result.ForLLM != "updated alias/existing.txt\nadded alias/new.txt" {
		t.Fatalf("summary = %q", result.ForLLM)
	}
	if got := readApplyPatchFixture(t, workspace, "real/existing.txt"); got != "after\n" {
		t.Fatalf("existing content = %q", got)
	}
	if got := readApplyPatchFixture(t, workspace, "real/new.txt"); got != "new\n" {
		t.Fatalf("new content = %q", got)
	}
}

func TestApplyPatchPreflightProtectedRootOverridesOutsideAllowPath(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	patterns := []*regexp.Regexp{regexp.MustCompile(
		"^" + regexp.QuoteMeta(filepath.Clean(outside)) +
			"(?:" + regexp.QuoteMeta(string(os.PathSeparator)) + "|$)",
	)}
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		ProtectedRoots: []string{outside},
	}, patterns)
	target := filepath.Join(outside, "new.txt")
	result := executeApplyPatch(t, tool, context.Background(), fmt.Sprintf(
		"*** Begin Patch\n*** Add File: %s\n+forbidden\n*** End Patch", target,
	))
	requireApplyPatchError(t, result)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("protected allow-path target exists: %v", err)
	}
}

func TestApplyPatchPreflightProtectedAncestorDoesNotBlanketWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "editable-workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		ProtectedRoots: []string{home},
	})
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Add File: ordinary.txt\n+allowed\n*** End Patch",
	)
	if result.IsError {
		t.Fatalf("protected ancestor blanketed editable workspace: %s", result.ForLLM)
	}
	if got := readApplyPatchFixture(t, workspace, "ordinary.txt"); got != "allowed\n" {
		t.Fatalf("ordinary content = %q", got)
	}
}

func TestApplyPatchPreflightVolatileProtectedAncestorBlocksNestedWorkspace(t *testing.T) {
	archiveRoot := t.TempDir()
	workspace := filepath.Join(archiveRoot, "agent-workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		VolatileProtectedRoots: []string{archiveRoot},
	})
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Add File: ordinary.txt\n+denied\n*** End Patch",
	)
	requireApplyPatchError(t, result, "protected")
	if _, err := os.Stat(filepath.Join(workspace, "ordinary.txt")); !os.IsNotExist(err) {
		t.Fatalf("volatile protected ancestor allowed nested workspace mutation: %v", err)
	}
}

func TestApplyPatchPreflightVolatileProtectedRootAllowsRuntimeReplacement(t *testing.T) {
	workspace := t.TempDir()
	protected := writeApplyPatchFixture(
		t, workspace, "launcher-auth.db-wal", "before\n", 0o600,
	)
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		VolatileProtectedRoots: []string{protected},
	})
	if err := os.Remove(protected); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protected, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Add File: ordinary.txt\n+allowed\n*** End Patch",
	)
	if result.IsError {
		t.Fatalf("runtime root replacement stale-blocked ordinary patch: %s", result.ForLLM)
	}
	result = executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Update File: launcher-auth.db-wal\n"+
			"@@\n-replacement\n+mutated\n*** End Patch",
	)
	requireApplyPatchError(t, result, "protected")
	content, err := os.ReadFile(protected)
	if err != nil || string(content) != "replacement\n" {
		t.Fatalf("volatile protected content = %q, %v", content, err)
	}
}

func TestApplyPatchPreflightSharesDetachedPreparedVolatileRoots(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "runtime.db")
	if err := os.WriteFile(protected, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := []string{protected}
	prepared, err := NewPreparedApplyPatchVolatileRoots(workspace, roots)
	if err != nil {
		t.Fatal(err)
	}
	roots[0] = filepath.Join(workspace, "ordinary.txt")
	first, err := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		true,
		true,
		true,
		ApplyPatchPreflightPolicy{PreparedVolatileProtectedRoots: prepared},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		true,
		true,
		true,
		ApplyPatchPreflightPolicy{PreparedVolatileProtectedRoots: prepared},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.volatileRoots != prepared || second.volatileRoots != prepared {
		t.Fatal("apply_patch copied the prepared volatile-root policy")
	}
	result := first.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Update File: runtime.db\n@@\n-before\n+changed\n*** End Patch",
	})
	if result == nil || !result.IsError {
		t.Fatalf("prepared volatile root mutation = %#v", result)
	}
	alias := filepath.Join(workspace, "runtime.alias")
	if err := os.Link(protected, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	result = second.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Update File: runtime.alias\n@@\n-before\n+changed\n*** End Patch",
	})
	if result == nil || !result.IsError {
		t.Fatalf("prepared volatile-root hardlink mutation = %#v", result)
	}
	if mixed, mixedErr := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		true,
		true,
		true,
		ApplyPatchPreflightPolicy{
			VolatileProtectedRoots:         []string{protected},
			PreparedVolatileProtectedRoots: prepared,
		},
	); mixedErr == nil || mixed != nil {
		t.Fatalf("mixed prepared/source volatile roots = %#v, %v", mixed, mixedErr)
	}
}

func TestApplyPatchPreflightPolicyRejectsInvalidRuntimeRoots(t *testing.T) {
	workspace := t.TempDir()
	transactionRoot := filepath.Join(t.TempDir(), "apply-patch-transactions")
	for _, test := range []struct {
		name   string
		policy ApplyPatchPreflightPolicy
	}{
		{
			name: "transaction state",
			policy: ApplyPatchPreflightPolicy{
				TransactionStateRoot: "invalid\x00root",
			},
		},
		{
			name: "volatile protected root",
			policy: ApplyPatchPreflightPolicy{
				TransactionStateRoot:   transactionRoot,
				VolatileProtectedRoots: []string{"invalid\x00root"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
				workspace,
				true,
				true,
				true,
				test.policy,
			)
			if err == nil || tool != nil {
				t.Fatalf("invalid runtime roots returned %#v, %v", tool, err)
			}
		})
	}
}

func TestApplyPatchPreflightProtectedAncestorStillBlocksAllowedSibling(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "editable-workspace")
	private := filepath.Join(home, "private-control")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	patterns := []*regexp.Regexp{regexp.MustCompile(
		"^" + regexp.QuoteMeta(filepath.Clean(private)) +
			"(?:" + regexp.QuoteMeta(string(os.PathSeparator)) + "|$)",
	)}
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		ProtectedRoots: []string{home},
	}, patterns)
	target := filepath.Join(private, "secret.txt")
	result := executeApplyPatch(t, tool, context.Background(), fmt.Sprintf(
		"*** Begin Patch\n*** Add File: %s\n+forbidden\n*** End Patch", target,
	))
	requireApplyPatchError(t, result)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("protected sibling target exists: %v", err)
	}
}

func TestApplyPatchPreflightOutsideAllowPathCompatibility(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	patterns := []*regexp.Regexp{regexp.MustCompile(
		"^" + regexp.QuoteMeta(filepath.Clean(outside)) +
			"(?:" + regexp.QuoteMeta(string(os.PathSeparator)) + "|$)",
	)}
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{}, patterns,
	)
	target := filepath.Join(outside, "new.txt")
	result := executeApplyPatch(t, tool, context.Background(), fmt.Sprintf(
		"*** Begin Patch\n*** Add File: %s\n+allowed\n*** End Patch", target,
	))
	if result.IsError {
		t.Fatalf("outside allow-path patch failed: %s", result.ForLLM)
	}
	if result.ForLLM != "added "+target {
		t.Fatalf("summary = %q", result.ForLLM)
	}
	if got := readApplyPatchFixture(t, outside, "new.txt"); got != "allowed\n" {
		t.Fatalf("outside content = %q", got)
	}
}

func TestApplyPatchPreflightRejectsDuplicateAndMoveGraphs(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		patch func(string) string
	}{
		{
			name:  "duplicate source",
			setup: func(t *testing.T, root string) { writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640) },
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: a.txt\n@@\n-a\n+A\n" +
					"*** Update File: ./a.txt\n@@\n-a\n+B\n*** End Patch"
			},
		},
		{
			name:  "cleaned alias",
			setup: func(t *testing.T, root string) { writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640) },
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: dir/../a.txt\n@@\n-a\n+A\n" +
					"*** Delete File: a.txt\n*** End Patch"
			},
		},
		{
			name:  "absolute relative alias",
			setup: func(t *testing.T, root string) { writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640) },
			patch: func(root string) string {
				return fmt.Sprintf("*** Begin Patch\n"+
					"*** Update File: a.txt\n@@\n-a\n+A\n"+
					"*** Delete File: %s\n*** End Patch", filepath.Join(root, "a.txt"))
			},
		},
		{
			name: "duplicate add target",
			patch: func(string) string {
				return "*** Begin Patch\n*** Add File: a.txt\n+a\n*** Add File: ./a.txt\n+b\n*** End Patch"
			},
		},
		{
			name: "symlink aliases to duplicate add target",
			setup: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
					t.Fatal(err)
				}
				for _, alias := range []string{"alias-one", "alias-two"} {
					if err := os.Symlink("real", filepath.Join(root, alias)); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				}
			},
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Add File: alias-one/new.txt\n+one\n" +
					"*** Add File: alias-two/new.txt\n+two\n*** End Patch"
			},
		},
		{
			name:  "self move",
			setup: func(t *testing.T, root string) { writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640) },
			patch: func(string) string {
				return "*** Begin Patch\n*** Update File: a.txt\n*** Move to: ./a.txt\n*** End Patch"
			},
		},
		{
			name: "move target exists",
			setup: func(t *testing.T, root string) {
				writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640)
				writeApplyPatchFixture(t, root, "b.txt", "b\n", 0o600)
			},
			patch: func(string) string {
				return "*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n*** End Patch"
			},
		},
		{
			name: "move fan in",
			setup: func(t *testing.T, root string) {
				writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640)
				writeApplyPatchFixture(t, root, "b.txt", "b\n", 0o600)
			},
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: a.txt\n*** Move to: c.txt\n" +
					"*** Update File: b.txt\n*** Move to: c.txt\n*** End Patch"
			},
		},
		{
			name: "move chain",
			setup: func(t *testing.T, root string) {
				writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640)
				writeApplyPatchFixture(t, root, "b.txt", "b\n", 0o600)
			},
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: a.txt\n*** Move to: b.txt\n" +
					"*** Update File: b.txt\n*** Move to: c.txt\n*** End Patch"
			},
		},
		{
			name: "move cycle",
			setup: func(t *testing.T, root string) {
				writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640)
				writeApplyPatchFixture(t, root, "b.txt", "b\n", 0o600)
			},
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: a.txt\n*** Move to: b.txt\n" +
					"*** Update File: b.txt\n*** Move to: a.txt\n*** End Patch"
			},
		},
		{
			name:  "move destination also add target",
			setup: func(t *testing.T, root string) { writeApplyPatchFixture(t, root, "a.txt", "a\n", 0o640) },
			patch: func(string) string {
				return "*** Begin Patch\n" +
					"*** Update File: a.txt\n*** Move to: c.txt\n" +
					"*** Add File: c.txt\n+c\n*** End Patch"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			if test.setup != nil {
				test.setup(t, workspace)
			}
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch(workspace))
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightRejectsAncestorDescendantFileRoles(t *testing.T) {
	for _, paths := range [][2]string{
		{"parent", "parent/child.txt"},
		{"parent/child.txt", "parent"},
	} {
		t.Run(strings.Join(paths[:], " then "), func(t *testing.T) {
			workspace := t.TempDir()
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			patch := fmt.Sprintf(
				"*** Begin Patch\n*** Add File: %s\n+first\n"+
					"*** Add File: %s\n+second\n*** End Patch",
				paths[0],
				paths[1],
			)
			result := executeApplyPatch(t, tool, context.Background(), patch)
			requireApplyPatchError(t, result, "conflict")
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightRejectsHardLinkedSources(t *testing.T) {
	tests := []struct {
		name  string
		patch string
	}{
		{
			name:  "update",
			patch: "*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
		},
		{
			name:  "delete",
			patch: "*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
		},
		{
			name:  "move",
			patch: "*** Begin Patch\n*** Update File: source.txt\n*** Move to: moved.txt\n*** End Patch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			source := writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o751)
			alias := filepath.Join(workspace, "alias.txt")
			if err := os.Link(source, alias); err != nil {
				t.Skipf("hard links unavailable: %v", err)
			}
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			requireApplyPatchError(t, result, "link")
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightRevalidationRejectsNewHardlink(t *testing.T) {
	workspace := t.TempDir()
	source := writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o751)
	alias := filepath.Join(workspace, "late-alias.txt")
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	tool.beforeRevalidate = func(*applyPatchPlan) {
		if err := os.Link(source, alias); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
	)
	requireApplyPatchError(t, result, "link")
	if got := readApplyPatchFixture(t, workspace, "source.txt"); got != "before\n" {
		t.Fatalf("source changed through new hardlink: %q", got)
	}
	if got := readApplyPatchFixture(t, workspace, "late-alias.txt"); got != "before\n" {
		t.Fatalf("late hardlink content = %q", got)
	}
}

func TestApplyPatchPreflightRejectsDirectorySourcesAndTargets(t *testing.T) {
	tests := []struct {
		name  string
		patch string
	}{
		{
			name:  "update directory",
			patch: "*** Begin Patch\n*** Update File: directory\n@@\n-old\n+new\n*** End Patch",
		},
		{
			name:  "delete empty directory",
			patch: "*** Begin Patch\n*** Delete File: directory\n*** End Patch",
		},
		{
			name:  "add over directory",
			patch: "*** Begin Patch\n*** Add File: directory\n+new\n*** End Patch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.Mkdir(filepath.Join(workspace, "directory"), 0o751); err != nil {
				t.Fatal(err)
			}
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightHunkFailuresLeaveExactTree(t *testing.T) {
	tests := []struct {
		name    string
		content string
		patch   string
	}{
		{
			name:    "later hunk missing",
			content: "one\ntwo\nthree\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n" +
				"@@ first\n-one\n+ONE\n" +
				"@@ second\n-missing\n+MISSING\n*** End Patch",
		},
		{
			name:    "first hunk ambiguous",
			content: "repeat\nrepeat\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n" +
				"@@ repeated\n-repeat\n+changed\n*** End Patch",
		},
		{
			name:    "later hunk ambiguous after virtual edit",
			content: "one\ntwo\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n" +
				"@@ first\n-one\n+two\n" +
				"@@ second\n-two\n+done\n*** End Patch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "file.txt", test.content, 0o751)
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchParserMarkerLikeContextIsData(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "markers.txt",
		"@@ literal\n*** Move to: literal\n*** Update File: literal\nold\n", 0o640,
	)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Update File: markers.txt\n@@ section\n"+
			" @@ literal\n"+
			" *** Move to: literal\n"+
			" *** Update File: literal\n"+
			"-old\n+new\n*** End Patch",
	)
	if result.IsError {
		t.Fatalf("marker-like context patch failed: %s", result.ForLLM)
	}
	if got := readApplyPatchFixture(t, workspace, "markers.txt"); got !=
		"@@ literal\n*** Move to: literal\n*** Update File: literal\nnew\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestApplyPatchParserCRLFEnvelopeMatchesLF(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: file.txt\n@@ section\n-before\n+after\n*** End Patch"
	results := make([]*ToolResult, 0, 2)
	for _, encoded := range []string{patch, strings.ReplaceAll(patch, "\n", "\r\n")} {
		workspace := t.TempDir()
		writeApplyPatchFixture(t, workspace, "file.txt", "before\n", 0o640)
		tool := newApplyPatchPreflightTestTool(
			t, workspace, true, true, ApplyPatchPreflightPolicy{},
		)
		result := executeApplyPatch(t, tool, context.Background(), encoded)
		if result.IsError {
			t.Fatalf("patch failed: %s", result.ForLLM)
		}
		if got := readApplyPatchFixture(t, workspace, "file.txt"); got != "after\n" {
			t.Fatalf("content = %q", got)
		}
		results = append(results, result)
	}
	if results[0].ForLLM != results[1].ForLLM ||
		results[0].ForUser != results[1].ForUser ||
		results[0].IsError != results[1].IsError ||
		results[0].Silent != results[1].Silent ||
		results[0].Async != results[1].Async ||
		results[0].ResponseHandled != results[1].ResponseHandled {
		t.Fatalf("LF result %#v != CRLF result %#v", results[0], results[1])
	}
}

func TestApplyPatchParserNoNewlineAndEndOfFile(t *testing.T) {
	tests := []struct {
		name    string
		before  string
		patch   string
		want    string
		wantErr bool
	}{
		{
			name:   "replace exact no-final-newline",
			before: "before",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"-before\n\\ No newline at end of file\n" +
				"+after\n\\ No newline at end of file\n*** End Patch",
			want: "after",
		},
		{
			name:   "add final newline",
			before: "before",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"-before\n\\ No newline at end of file\n" +
				"+after\n*** End Patch",
			want: "after\n",
		},
		{
			name:   "remove final newline",
			before: "before\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"-before\n" +
				"+after\n\\ No newline at end of file\n*** End Patch",
			want: "after",
		},
		{
			name:   "EOF replacement",
			before: "head\nend\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@ tail\n" +
				"-end\n+tail\n*** End of File\n*** End Patch",
			want: "head\ntail\n",
		},
		{
			name:   "EOF replacement context remains unique",
			before: "end\nend\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@ tail\n" +
				"-end\n+tail\n*** End of File\n*** End Patch",
			wantErr: true,
		},
		{
			name:   "EOF insertion",
			before: "head\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@ tail\n" +
				"+tail\n*** End of File\n*** End Patch",
			want: "head\ntail\n",
		},
		{
			name:    "pure insertion without EOF is ambiguous",
			before:  "head\n",
			patch:   "*** Begin Patch\n*** Update File: file.txt\n@@\n+tail\n*** End Patch",
			wantErr: true,
		},
		{
			name:    "empty hunk",
			before:  "head\n",
			patch:   "*** Begin Patch\n*** Update File: file.txt\n@@\n*** End Patch",
			wantErr: true,
		},
		{
			name:   "misplaced no-newline marker",
			before: "head\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"\\ No newline at end of file\n-head\n+tail\n*** End Patch",
			wantErr: true,
		},
		{
			name:   "duplicate no-newline marker",
			before: "head",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"-head\n\\ No newline at end of file\n\\ No newline at end of file\n" +
				"+tail\n*** End Patch",
			wantErr: true,
		},
		{
			name:   "old side continues after no-newline marker",
			before: "ab\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"-a\n\\ No newline at end of file\n-b\n+replacement\n*** End Patch",
			wantErr: true,
		},
		{
			name:   "new side continues after no-newline marker",
			before: "old\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"-old\n+a\n\\ No newline at end of file\n+b\n*** End Patch",
			wantErr: true,
		},
		{
			name:   "duplicate EOF marker",
			before: "head\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"+tail\n*** End of File\n*** End of File\n*** End Patch",
			wantErr: true,
		},
		{
			name:   "EOF context does not reach EOF",
			before: "end\ntrailing\n",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"-end\n+tail\n*** End of File\n*** End Patch",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "file.txt", test.before, 0o751)
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			if test.wantErr {
				requireApplyPatchError(t, result)
				assertApplyPatchTreeEqual(t, workspace, before)
				return
			}
			if result.IsError {
				t.Fatalf("patch failed: %s", result.ForLLM)
			}
			if got := readApplyPatchFixture(t, workspace, "file.txt"); got != test.want {
				t.Fatalf("content = %q, want %q", got, test.want)
			}
		})
	}
}

func TestApplyPatchParserRejectsAmbiguousStructures(t *testing.T) {
	tests := []struct {
		name  string
		patch string
	}{
		{
			name:  "update without hunk or move",
			patch: "*** Begin Patch\n*** Update File: file.txt\n*** End Patch",
		},
		{
			name: "second move directive",
			patch: "*** Begin Patch\n*** Update File: file.txt\n" +
				"*** Move to: first.txt\n*** Move to: second.txt\n*** End Patch",
		},
		{
			name: "blank then real move directive",
			patch: "*** Begin Patch\n*** Update File: file.txt\n" +
				"*** Move to:   \n*** Move to: second.txt\n*** End Patch",
		},
		{
			name: "blank move with hunk",
			patch: "*** Begin Patch\n*** Update File: file.txt\n" +
				"*** Move to:   \n@@\n-before\n+after\n*** End Patch",
		},
		{
			name: "EOF followed by hunk data",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"-before\n+after\n*** End of File\n+later\n*** End Patch",
		},
		{
			name:  "context-only hunk",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n before\n*** End Patch",
		},
		{
			name:  "add content without plus",
			patch: "*** Begin Patch\n*** Add File: added.txt\nplain\n*** End Patch",
		},
		{
			name: "invalid hunk prefix",
			patch: "*** Begin Patch\n*** Update File: file.txt\n@@\n" +
				"?before\n*** End Patch",
		},
		{
			name: "EOF marker outside hunk",
			patch: "*** Begin Patch\n*** Update File: file.txt\n" +
				"*** End of File\n*** End Patch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "file.txt", "before\n", 0o751)
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchMoveOnlyAndEmptyAddCompatibility(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "source\n", 0o751)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Update File: source.txt\n*** Move to: moved.txt\n"+
			"*** Add File: empty.txt\n"+
			"*** End Patch",
	)
	if result.IsError {
		t.Fatalf("move-only/empty-add patch failed: %s", result.ForLLM)
	}
	if result.ForLLM != "moved source.txt to moved.txt\nadded empty.txt" {
		t.Fatalf("summary = %q", result.ForLLM)
	}
	if got := readApplyPatchFixture(t, workspace, "moved.txt"); got != "source\n" {
		t.Fatalf("moved content = %q", got)
	}
	if got := readApplyPatchFixture(t, workspace, "empty.txt"); got != "" {
		t.Fatalf("empty add content = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "source.txt")); !os.IsNotExist(err) {
		t.Fatalf("move source remains: %v", err)
	}
}

func TestApplyPatchNestedDestinationCompatibility(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "source\n", 0o751)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Add File: added/deep/file.txt\n+added\n"+
			"*** Update File: source.txt\n*** Move to: moved/deep/file.txt\n"+
			"*** End Patch",
	)
	if result.IsError {
		t.Fatalf("nested destination patch failed: %s", result.ForLLM)
	}
	if result.ForLLM != "added added/deep/file.txt\nmoved source.txt to moved/deep/file.txt" {
		t.Fatalf("summary = %q", result.ForLLM)
	}
	if got := readApplyPatchFixture(t, workspace, "added/deep/file.txt"); got != "added\n" {
		t.Fatalf("nested add content = %q", got)
	}
	if got := readApplyPatchFixture(t, workspace, "moved/deep/file.txt"); got != "source\n" {
		t.Fatalf("nested move content = %q", got)
	}
}

func TestApplyPatchCompatibilityExactSummaryAndRegistryParity(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Add File: added.txt\n+added\n" +
		"*** Update File: updated.txt\n@@\n-before\n+after\n" +
		"*** Delete File: deleted.txt\n" +
		"*** Update File: source.txt\n*** Move to: moved.txt\n" +
		"*** End Patch"
	wantSummary := "added added.txt\nupdated updated.txt\ndeleted deleted.txt\nmoved source.txt to moved.txt"

	type projection struct {
		ForLLM, ForUser        string
		Silent, IsError, Async bool
		ResponseHandled        bool
	}
	project := func(result *ToolResult) projection {
		return projection{
			ForLLM: result.ForLLM, ForUser: result.ForUser,
			Silent: result.Silent, IsError: result.IsError, Async: result.Async,
			ResponseHandled: result.ResponseHandled,
		}
	}
	var got []projection
	for _, surface := range []string{"direct", "registry", "owner factory"} {
		t.Run(surface, func(t *testing.T) {
			workspace := t.TempDir()
			transactionStateRoot := filepath.Join(
				t.TempDir(),
				"apply-patch-transactions",
			)
			writeApplyPatchFixture(t, workspace, "updated.txt", "before\n", 0o751)
			writeApplyPatchFixture(t, workspace, "deleted.txt", "delete\n", 0o640)
			writeApplyPatchFixture(t, workspace, "source.txt", "move\n", 0o700)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{
					TransactionStateRoot: transactionStateRoot,
				},
			)
			var result *ToolResult
			switch surface {
			case "registry":
				registry := NewToolRegistry()
				registry.Register(tool)
				result = registry.Execute(context.Background(), "apply_patch", map[string]any{"patch": patch})
			case "owner factory":
				factory, err := NewToolFactoryFromPrototype(tool, ToolTraits{
					Risk:        ToolRiskDestructive,
					Parallel:    ToolParallelSerialized,
					Idempotency: ToolIdempotencyNonIdempotent,
					Sharing:     ToolSharingPerOwner,
				}, func(ToolBuildContext) (Tool, error) {
					return NewApplyPatchToolWithPermissionsAndPolicy(
						workspace, true, true, true, ApplyPatchPreflightPolicy{
							TransactionStateRoot: transactionStateRoot,
						},
					)
				})
				if err != nil {
					t.Fatal(err)
				}
				source := NewToolRegistry()
				if registerErr := source.RegisterFactoryBacked(tool, factory); registerErr != nil {
					t.Fatal(registerErr)
				}
				defer func() {
					if closeErr := source.Close(); closeErr != nil {
						t.Errorf("close source registry: %v", closeErr)
					}
				}()
				owned, err := source.InstantiateForOwnerSelection(ToolOwner{
					Scope: ToolOwnerScopeAgent, AgentID: "apply-patch-parity",
				}, []string{"apply_patch"})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := owned.Close(); err != nil {
						t.Errorf("close owned registry: %v", err)
					}
				}()
				result = owned.Execute(context.Background(), "apply_patch", map[string]any{"patch": patch})
			default:
				result = executeApplyPatch(t, tool, context.Background(), patch)
			}
			if result == nil || result.IsError {
				t.Fatalf("surface=%s result=%#v", surface, result)
			}
			if result.ForLLM != wantSummary || result.ForUser == "" ||
				result.Silent || result.Async || result.ResponseHandled {
				t.Fatalf("surface=%s result=%#v", surface, result)
			}
			if gotContent := readApplyPatchFixture(t, workspace, "added.txt"); gotContent != "added\n" {
				t.Fatalf("added content = %q", gotContent)
			}
			if gotContent := readApplyPatchFixture(t, workspace, "updated.txt"); gotContent != "after\n" {
				t.Fatalf("updated content = %q", gotContent)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Join(workspace, "updated.txt"))
				if err != nil {
					t.Fatalf("stat updated file: %v", err)
				}
				if info.Mode().Perm() != 0o751 {
					t.Fatalf("updated mode = %#o; want 0751", info.Mode().Perm())
				}
			}
			if gotContent := readApplyPatchFixture(t, workspace, "moved.txt"); gotContent != "move\n" {
				t.Fatalf("moved content = %q", gotContent)
			}
			for _, absent := range []string{"deleted.txt", "source.txt"} {
				if _, err := os.Lstat(filepath.Join(workspace, absent)); !os.IsNotExist(err) {
					t.Fatalf("%s remains: %v", absent, err)
				}
			}
			got = append(got, project(result))
		})
	}
	if len(got) != 3 || !reflect.DeepEqual(got[0], got[1]) || !reflect.DeepEqual(got[0], got[2]) {
		t.Fatalf("surface results differ: %#v", got)
	}
}

func TestApplyPatchCompatibilityDescriptorIsStable(t *testing.T) {
	tool := NewApplyPatchTool(t.TempDir(), true)
	if tool.Name() != "apply_patch" {
		t.Fatalf("Name() = %q", tool.Name())
	}
	const description = "Apply a Codex-style patch with Begin Patch/End Patch blocks. Supports add, delete, update, and move operations."
	if tool.Description() != description {
		t.Fatalf("Description() = %q", tool.Description())
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patch": map[string]any{
				"type":        "string",
				"description": "Patch text beginning with *** Begin Patch and ending with *** End Patch.",
			},
		},
		"required": []string{"patch"},
	}
	if got := tool.Parameters(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Parameters() = %#v, want %#v", got, want)
	}
}

func TestApplyPatchPreflightPlanCapturesDetachedBytesAndModes(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o751)
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	inspected := false
	tool.beforeRevalidate = func(plan *applyPatchPlan) {
		if plan == nil || len(plan.ops) != 2 {
			t.Fatalf("plan = %#v, want two operations", plan)
		}
		update := &plan.ops[0]
		if update.kind != "update" || update.source == nil ||
			string(update.source.data) != "before\n" || string(update.before) != "before\n" ||
			string(update.after) != "after\n" || update.mode != sourceInfo.Mode() {
			t.Fatalf("update plan = %#v", update)
		}
		update.before[0] = 'X'
		if string(update.source.data) != "before\n" || string(update.after) != "after\n" {
			t.Fatalf("plan byte slices alias: source=%q after=%q", update.source.data, update.after)
		}
		update.after[0] = 'Y'
		if string(update.source.data) != "before\n" {
			t.Fatalf("postimage aliases source snapshot: %q", update.source.data)
		}
		update.after[0] = 'a'
		add := &plan.ops[1]
		if add.kind != "add" || add.source != nil || len(add.before) != 0 ||
			string(add.after) != "added\n" || add.mode != applyPatchFileMode() {
			t.Fatalf("add plan = %#v", add)
		}
		inspected = true
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n"+
			"*** Update File: source.txt\n@@\n-before\n+after\n"+
			"*** Add File: added.txt\n+added\n"+
			"*** End Patch",
	)
	if result.IsError {
		t.Fatalf("patch failed: %s", result.ForLLM)
	}
	if !inspected {
		t.Fatal("beforeRevalidate hook did not inspect the plan")
	}
	if got := readApplyPatchFixture(t, workspace, "source.txt"); got != "after\n" {
		t.Fatalf("source content = %q", got)
	}
}

func TestApplyPatchPreflightRevalidationRejectsDrift(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, string) string
		drift  func(*testing.T, string, string)
		patch  string
		verify func(*testing.T, string, string)
	}{
		{
			name: "source bytes",
			setup: func(t *testing.T, root string) string {
				return writeApplyPatchFixture(t, root, "source.txt", "before\n", 0o751)
			},
			drift: func(t *testing.T, _, source string) {
				if err := os.WriteFile(source, []byte("external\n"), 0o751); err != nil {
					t.Fatal(err)
				}
			},
			patch: "*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
			verify: func(t *testing.T, root, _ string) {
				if got := readApplyPatchFixture(t, root, "source.txt"); got != "external\n" {
					t.Fatalf("source = %q, want external drift", got)
				}
			},
		},
		{
			name: "source mode",
			setup: func(t *testing.T, root string) string {
				return writeApplyPatchFixture(t, root, "source.txt", "before\n", 0o751)
			},
			drift: func(t *testing.T, _, source string) {
				if runtime.GOOS == "windows" {
					t.Skip("portable Windows mode bits do not express this drift")
				}
				if err := os.Chmod(source, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			patch: "*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
			verify: func(t *testing.T, root, _ string) {
				if got := readApplyPatchFixture(t, root, "source.txt"); got != "before\n" {
					t.Fatalf("source = %q", got)
				}
			},
		},
		{
			name:  "absent destination appears",
			setup: func(*testing.T, string) string { return "" },
			drift: func(t *testing.T, root, _ string) {
				writeApplyPatchFixture(t, root, "new.txt", "external\n", 0o600)
			},
			patch: "*** Begin Patch\n*** Add File: new.txt\n+planned\n*** End Patch",
			verify: func(t *testing.T, root, _ string) {
				if got := readApplyPatchFixture(t, root, "new.txt"); got != "external\n" {
					t.Fatalf("destination = %q", got)
				}
			},
		},
		{
			name: "source becomes directory",
			setup: func(t *testing.T, root string) string {
				return writeApplyPatchFixture(t, root, "source.txt", "before\n", 0o751)
			},
			drift: func(t *testing.T, _, source string) {
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(source, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			patch: "*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
			verify: func(t *testing.T, _, source string) {
				if info, err := os.Lstat(source); err != nil || !info.IsDir() {
					t.Fatalf("source drift = %#v, %v", info, err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			source := test.setup(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			tool.beforeRevalidate = func(*applyPatchPlan) {
				test.drift(t, workspace, source)
			}
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			requireApplyPatchError(t, result)
			test.verify(t, workspace, source)
		})
	}
}

func TestApplyPatchPreflightRevalidationRejectsAncestorSymlinkDrift(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "first/source.txt", "before\n", 0o751)
	writeApplyPatchFixture(t, workspace, "second/source.txt", "other\n", 0o640)
	alias := filepath.Join(workspace, "alias")
	if err := os.Symlink("first", alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	tool.beforeRevalidate = func(*applyPatchPlan) {
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("second", alias); err != nil {
			t.Fatal(err)
		}
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Update File: alias/source.txt\n@@\n-before\n+after\n*** End Patch",
	)
	requireApplyPatchError(t, result)
	if got := readApplyPatchFixture(t, workspace, "first/source.txt"); got != "before\n" {
		t.Fatalf("original canonical source changed to %q", got)
	}
	if got := readApplyPatchFixture(t, workspace, "second/source.txt"); got != "other\n" {
		t.Fatalf("replacement alias source changed to %q", got)
	}
	if target, err := os.Readlink(alias); err != nil || target != "second" {
		t.Fatalf("external alias drift = %q, %v", target, err)
	}
}

func TestApplyPatchPreflightRevalidationRejectsCanonicalAncestorEscape(t *testing.T) {
	workspace := t.TempDir()
	realDirectory := filepath.Join(workspace, "real")
	if err := os.Mkdir(realDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(workspace, "alias")
	if err := os.Symlink("real", alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	outside := t.TempDir()
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	tool.beforeRevalidate = func(*applyPatchPlan) {
		displaced := filepath.Join(workspace, "real-displaced")
		if err := os.Rename(realDirectory, displaced); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, realDirectory); err != nil {
			t.Fatal(err)
		}
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Add File: alias/new.txt\n+forbidden\n*** End Patch",
	)
	requireApplyPatchError(t, result)
	if _, err := os.Lstat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("canonical ancestor escape wrote outside: %v", err)
	}
	if target, err := os.Readlink(realDirectory); err != nil || target != outside {
		t.Fatalf("external canonical drift = %q, %v", target, err)
	}
}

func TestApplyPatchPreflightRejectsCanonicalDriftDuringFenceCapture(t *testing.T) {
	workspace := t.TempDir()
	realDirectory := filepath.Join(workspace, "real")
	if err := os.Mkdir(realDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	tool.beforePathFence = func(string) {
		if err := os.Remove(realDirectory); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, realDirectory); err != nil {
			t.Fatal(err)
		}
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Add File: real/new.txt\n+forbidden\n*** End Patch",
	)
	requireApplyPatchError(t, result, "changed")
	if _, err := os.Lstat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("fence-capture drift wrote outside: %v", err)
	}
	if target, err := os.Readlink(realDirectory); err != nil || target != outside {
		t.Fatalf("external fence-capture drift = %q, %v", target, err)
	}
}

func TestApplyPatchPreflightRevalidationRejectsProtectedRootSymlinkDrift(t *testing.T) {
	workspace := t.TempDir()
	firstProtected := filepath.Join(workspace, "protected-first")
	secondProtected := filepath.Join(workspace, "protected-second")
	if err := os.MkdirAll(firstProtected, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondProtected, 0o700); err != nil {
		t.Fatal(err)
	}
	protectedAlias := filepath.Join(workspace, "protected-alias")
	if err := os.Symlink("protected-first", protectedAlias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeApplyPatchFixture(t, workspace, "ordinary.txt", "before\n", 0o751)
	tool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		ProtectedRoots: []string{protectedAlias},
	})
	tool.beforeRevalidate = func(*applyPatchPlan) {
		if err := os.Remove(protectedAlias); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("protected-second", protectedAlias); err != nil {
			t.Fatal(err)
		}
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Update File: ordinary.txt\n@@\n-before\n+after\n*** End Patch",
	)
	requireApplyPatchError(t, result)
	if got := readApplyPatchFixture(t, workspace, "ordinary.txt"); got != "before\n" {
		t.Fatalf("ordinary source changed to %q", got)
	}
	if target, err := os.Readlink(protectedAlias); err != nil || target != "protected-second" {
		t.Fatalf("external protected-root drift = %q, %v", target, err)
	}
}

func TestApplyPatchPreflightRevalidationRejectsWorkspaceIdentityDrift(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o751)
	displaced := filepath.Join(parent, "displaced-workspace")
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	tool.beforeRevalidate = func(*applyPatchPlan) {
		if err := os.Rename(workspace, displaced); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		writeApplyPatchFixture(t, workspace, "source.txt", "replacement\n", 0o751)
	}
	result := executeApplyPatch(t, tool, context.Background(),
		"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
	)
	requireApplyPatchError(t, result)
	if got := readApplyPatchFixture(t, displaced, "source.txt"); got != "before\n" {
		t.Fatalf("displaced source changed to %q", got)
	}
	if got := readApplyPatchFixture(t, workspace, "source.txt"); got != "replacement\n" {
		t.Fatalf("replacement workspace source changed to %q", got)
	}
}

func TestApplyPatchPreflightWorkspaceGateCanonicalizesAliases(t *testing.T) {
	workspace := t.TempDir()
	transactionStateRoot := filepath.Join(t.TempDir(), "apply-patch-transactions")
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	firstGuardEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	}()
	firstTool := newApplyPatchPreflightTestTool(t, workspace, true, true, ApplyPatchPreflightPolicy{
		TransactionStateRoot: transactionStateRoot,
		PathGuard: func(string) error {
			select {
			case <-firstGuardEntered:
			default:
				close(firstGuardEntered)
			}
			<-releaseFirst
			return nil
		},
	})
	secondGuardEntered := make(chan struct{})
	secondTool := newApplyPatchPreflightTestTool(t, alias, true, true, ApplyPatchPreflightPolicy{
		TransactionStateRoot: transactionStateRoot,
		PathGuard: func(string) error {
			close(secondGuardEntered)
			return nil
		},
	})
	firstResult := make(chan *ToolResult, 1)
	go func() {
		firstResult <- firstTool.Execute(context.Background(), map[string]any{
			"patch": "*** Begin Patch\n*** Add File: first.txt\n+first\n*** End Patch",
		})
	}()
	select {
	case <-firstGuardEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first planner did not enter its workspace gate")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondResult := make(chan *ToolResult, 1)
	go func() {
		secondResult <- secondTool.Execute(ctx, map[string]any{
			"patch": "*** Begin Patch\n*** Add File: second.txt\n+second\n*** End Patch",
		})
	}()
	select {
	case <-secondGuardEntered:
		t.Fatal("workspace alias acquired a distinct gate")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case result := <-secondResult:
		requireApplyPatchError(t, result, "cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("gate waiter did not observe cancellation")
	}
	close(releaseFirst)
	select {
	case result := <-firstResult:
		if result == nil || result.IsError {
			t.Fatalf("first result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first planner did not finish")
	}
	if got := readApplyPatchFixture(t, workspace, "first.txt"); got != "first\n" {
		t.Fatalf("first content = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "second.txt")); !os.IsNotExist(err) {
		t.Fatalf("canceled waiter mutated second.txt: %v", err)
	}
	thirdTool := newApplyPatchPreflightTestTool(
		t, alias, true, true, ApplyPatchPreflightPolicy{
			TransactionStateRoot: transactionStateRoot,
		},
	)
	third := executeApplyPatch(t, thirdTool, context.Background(),
		"*** Begin Patch\n*** Add File: third.txt\n+third\n*** End Patch",
	)
	if third.IsError {
		t.Fatalf("gate remained wedged after cancellation: %s", third.ForLLM)
	}
}

func TestApplyPatchPreflightWorkspaceGatesDoNotSerializeUnrelatedRoots(t *testing.T) {
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	transactionStateRoot := filepath.Join(t.TempDir(), "apply-patch-transactions")
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	first := newApplyPatchPreflightTestTool(t, firstWorkspace, true, true, ApplyPatchPreflightPolicy{
		TransactionStateRoot: transactionStateRoot,
		PathGuard: func(string) error {
			close(entered)
			<-release
			return nil
		},
	})
	second := newApplyPatchPreflightTestTool(
		t, secondWorkspace, true, true, ApplyPatchPreflightPolicy{
			TransactionStateRoot: transactionStateRoot,
		},
	)
	firstResult := make(chan *ToolResult, 1)
	go func() {
		firstResult <- first.Execute(context.Background(), map[string]any{
			"patch": "*** Begin Patch\n*** Add File: first.txt\n+first\n*** End Patch",
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first workspace did not enter guard")
	}
	secondResult := make(chan *ToolResult, 1)
	go func() {
		secondResult <- second.Execute(context.Background(), map[string]any{
			"patch": "*** Begin Patch\n*** Add File: second.txt\n+second\n*** End Patch",
		})
	}()
	select {
	case result := <-secondResult:
		if result == nil || result.IsError {
			t.Fatalf("second result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated workspace was serialized behind first")
	}
	close(release)
	select {
	case result := <-firstResult:
		if result == nil || result.IsError {
			t.Fatalf("first result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first workspace did not finish")
	}
}

func TestApplyPatchPlanningPrimitivesCheckCancellationInChunks(t *testing.T) {
	haystack := []byte(strings.Repeat("a", 512*1024))
	matchCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 3,
	}
	_, _, matchErr := findUniqueApplyPatchMatch(matchCtx, haystack, []byte("missing"))
	if !errors.Is(matchErr, context.Canceled) {
		t.Fatalf("chunked unique match error = %v, want canceled", matchErr)
	}

	filePath := writeApplyPatchFixture(
		t,
		t.TempDir(),
		"large.txt",
		strings.Repeat("b", 512*1024),
		0o600,
	)
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	readCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 3,
	}
	if _, err := readApplyPatchSourceContext(readCtx, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("chunked source read error = %v, want canceled", err)
	}
	copyCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 3,
	}
	if _, err := appendApplyPatchBytesContext(copyCtx, nil, haystack); !errors.Is(err, context.Canceled) {
		t.Fatalf("chunked byte copy error = %v, want canceled", err)
	}
	equalCtx := &applyPatchCancelAfterChecksContext{
		Context: context.Background(), remaining: 3,
	}
	equalCopy := append([]byte(nil), haystack...)
	_, equalErr := equalApplyPatchBytesContext(equalCtx, haystack, equalCopy)
	if !errors.Is(equalErr, context.Canceled) {
		t.Fatalf("chunked byte equality error = %v, want canceled", equalErr)
	}
}
