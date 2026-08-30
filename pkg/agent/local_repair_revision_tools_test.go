package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestLocalRepairRevisionReadAndRangeEditPreserveUntouchedBytes(t *testing.T) {
	t.Parallel()

	pin, workspace, root := newLocalRepairTestWorkspace(t)
	_ = pin
	path := filepath.Join(root, "mixed.txt")
	before := []byte("alpha\nbeta\r\ngamma\nomega")
	if err := os.WriteFile(path, before, 0o755); err != nil {
		t.Fatal(err)
	}
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	read := newLocalRepairRevisionReadTool(guard)
	readResult := read.Execute(context.Background(), map[string]any{
		"path": "mixed.txt", "start_line": 2, "max_lines": 2,
	})
	if readResult == nil || readResult.IsError {
		t.Fatalf("revision read failed: %#v", readResult)
	}
	revision := localRepairRevisionFromResult(readResult.ForLLM)
	if !validLocalRepairOpaqueDigest(revision) {
		t.Fatalf("revision read result = %q", readResult.ForLLM)
	}
	wantDigest := sha256.Sum256(before)
	if revision != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("revision = %q, want whole-file digest %x", revision, wantDigest)
	}

	edit := newLocalRepairRevisionEditTool(guard)
	editResult := edit.Execute(context.Background(), map[string]any{
		"path":              "mixed.txt",
		"start_line":        2,
		"end_line":          3,
		"expected_revision": revision,
		"new_text":          "BETA\r\nGAMMA\n",
	})
	if editResult == nil || editResult.IsError {
		t.Fatalf("range edit failed: %#v", editResult)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "alpha\nBETA\r\nGAMMA\nomega"; string(after) != want {
		t.Fatalf("range edit = %q, want %q", after, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("edited mode = %#o, want %#o", info.Mode().Perm(), os.FileMode(0o755))
	}
}

func TestWriteLocalRepairEditableFileRejectsChangedTargetAndCleansTemporary(t *testing.T) {
	t.Parallel()

	pin, workspace, root := newLocalRepairTestWorkspace(t)
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	err = writeLocalRepairEditableFile(
		guard,
		"README.md",
		[]byte("must not replace\n"),
		0o644,
		strings.Repeat("0", 64),
	)
	if !errors.Is(err, errLocalRepairStaleRevision) {
		t.Fatalf("stale pre-rename write error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(content) != "before\n" {
		t.Fatalf("stale pre-rename write changed target: %q, %v", content, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".picoclaw-repair-") {
			t.Fatalf("stale write left temporary file %q", entry.Name())
		}
	}
}

func TestLocalRepairExactEditRejectsEmptyOldTextInsertion(t *testing.T) {
	t.Parallel()

	pin, workspace, root := newLocalRepairTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	result := newLocalRepairRevisionEditTool(guard).Execute(
		context.Background(),
		map[string]any{"path": "empty.txt", "old_text": "", "new_text": "insertion"},
	)
	if result == nil || !result.IsError {
		t.Fatalf("empty old_text insertion result = %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "empty.txt"))
	if err != nil || len(content) != 0 {
		t.Fatalf("empty old_text insertion changed file: %q, %v", content, err)
	}
}

func TestLocalRepairRevisionEditRejectsStaleRevisionBeforeWrite(t *testing.T) {
	t.Parallel()

	pin, workspace, root := newLocalRepairTestWorkspace(t)
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	read := newLocalRepairRevisionReadTool(guard)
	result := read.Execute(context.Background(), map[string]any{"path": "README.md"})
	revision := localRepairRevisionFromResult(result.ForLLM)
	external := []byte("external\r\nbytes\n")
	if writeErr := os.WriteFile(filepath.Join(root, "README.md"), external, 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	editResult := newLocalRepairRevisionEditTool(guard).Execute(
		context.Background(),
		map[string]any{
			"path":              "README.md",
			"start_line":        1,
			"end_line":          1,
			"expected_revision": revision,
			"new_text":          "after\n",
		},
	)
	if editResult == nil || !editResult.IsError ||
		!strings.Contains(editResult.ForLLM, "stale revision") {
		t.Fatalf("stale edit result = %#v", editResult)
	}
	after, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(external) {
		t.Fatalf("stale edit wrote file: %q", after)
	}
}

func TestLocalRepairRevisionEditModesAreExclusiveAndBounded(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	revision := hex.EncodeToString(digest[:])
	tool := newLocalRepairRevisionEditTool(guard)

	for name, args := range map[string]map[string]any{
		"both modes": {
			"path": "README.md", "old_text": "before\n", "start_line": 1,
			"end_line": 1, "expected_revision": revision, "new_text": "after\n",
		},
		"neither mode": {"path": "README.md", "new_text": "after\n"},
		"range beyond file": {
			"path": "README.md", "start_line": 2, "end_line": 2,
			"expected_revision": revision, "new_text": "after\n",
		},
		"range extra argument": {
			"path": "README.md", "start_line": 1, "end_line": 1,
			"expected_revision": revision, "new_text": "after\n", "extra": true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := tool.Execute(context.Background(), args)
			if result == nil || !result.IsError {
				t.Fatalf("invalid edit result = %#v", result)
			}
		})
	}
	after, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(after) != "before\n" {
		t.Fatalf("invalid edit changed file: %q, %v", after, err)
	}
}

func TestLocalRepairExactEditRetainsExistingMode(t *testing.T) {
	t.Parallel()

	pin, workspace, root := newLocalRepairTestWorkspace(t)
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	result := newLocalRepairRevisionEditTool(guard).Execute(
		context.Background(),
		map[string]any{
			"path": "README.md", "old_text": "before\n", "new_text": "after\n",
		},
	)
	if result == nil || result.IsError {
		t.Fatalf("exact edit failed: %#v", result)
	}
	after, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(after) != "after\n" {
		t.Fatalf("exact edit = %q, %v", after, err)
	}
}

func TestLocalRepairSchemasAreRevisionAwareOnly(t *testing.T) {
	t.Parallel()

	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := tools.NewEditFileTool(workspace.Path, true).Parameters()
	local := newLocalRepairRevisionEditTool(guard).Parameters()
	ordinaryJSON := toolSchemaJSON(t, ordinary)
	localJSON := toolSchemaJSON(t, local)
	if strings.Contains(ordinaryJSON, "expected_revision") ||
		!strings.Contains(localJSON, "expected_revision") ||
		!strings.Contains(localJSON, "start_line") ||
		!strings.Contains(localJSON, "end_line") ||
		strings.Contains(localJSON, "oneOf") || strings.Contains(localJSON, "anyOf") ||
		strings.Contains(localJSON, "pattern") || strings.Contains(localJSON, "minimum") {
		t.Fatalf("ordinary/local edit schemas = %s / %s", ordinaryJSON, localJSON)
	}
}

func TestLocalRepairEditSupportsNearNameLimitTarget(t *testing.T) {
	t.Parallel()

	pin, workspace, root := newLocalRepairTestWorkspace(t)
	name := strings.Repeat("n", 240) + ".txt"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Skipf("filesystem does not admit the long-name fixture: %v", err)
	}
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	result := newLocalRepairRevisionEditTool(guard).Execute(
		context.Background(),
		map[string]any{"path": name, "old_text": "before\n", "new_text": "after\n"},
	)
	if result == nil || result.IsError {
		t.Fatalf("long-name edit failed: %#v", result)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "after\n" {
		t.Fatalf("long-name edit = %q, %v", content, err)
	}
}

func TestLocalRepairListDirectoryDefaultsToRepositoryRoot(t *testing.T) {
	t.Parallel()

	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	registry := newLocalRepairToolRegistry(guard)
	result := registry.Execute(context.Background(), "list_dir", map[string]any{})
	if result == nil || result.IsError || !strings.Contains(result.ForLLM, "FILE: README.md") {
		t.Fatalf("root list result = %#v", result)
	}
	list, ok := registry.Get("list_dir")
	if !ok {
		t.Fatal("list_dir is unavailable")
	}
	if strings.Contains(toolSchemaJSON(t, list.Parameters()), `"required":["path"]`) {
		t.Fatalf("local list_dir schema still requires path: %#v", list.Parameters())
	}
}

func TestLocalRepairRevisionByteBudgetIsBounded(t *testing.T) {
	t.Parallel()

	var consumed atomic.Int64
	consumed.Store(maxLocalRepairRevisionBytesPerRun - 3)
	if !reserveLocalRepairRevisionBytes(&consumed, 2) ||
		reserveLocalRepairRevisionBytes(&consumed, 2) ||
		reserveLocalRepairRevisionBytes(&consumed, -1) ||
		reserveLocalRepairRevisionBytes(nil, 1) ||
		consumed.Load() != maxLocalRepairRevisionBytesPerRun-1 {
		t.Fatalf("revision byte budget = %d", consumed.Load())
	}
}

func toolSchemaJSON(t *testing.T, value map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
