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

func TestLocalRepairFileToolsDenyRuntimeProtectedRoots(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	database := filepath.Join(root, ".picoclaw", "launcher-auth.db")
	archiveRoot := filepath.Join(root, "legacy-json")
	archive := filepath.Join(archiveRoot, "launcher-auth-v1", "launcher-config.json")
	for _, path := range []string{database, archive} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if guard, guardErr := newLocalRepairPathGuard(
		workspace,
		pin,
		[]string{database, archiveRoot},
	); guardErr == nil || guard != nil {
		t.Fatalf("overlapping local-repair guard = %#v, %v", guard, guardErr)
	}
	for _, path := range []string{database, archive} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != "before\n" {
			t.Fatalf("protected local-repair content %q = %q, %v", path, content, readErr)
		}
	}

	disjointProtected := filepath.Join(t.TempDir(), "launcher-auth.db")
	if err := os.WriteFile(disjointProtected, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	guard, err := newLocalRepairPathGuard(workspace, pin, []string{disjointProtected})
	if err != nil {
		t.Fatalf("disjoint local-repair guard: %v", err)
	}
	ordinary := filepath.Join(root, "ordinary.txt")
	if writeErr := os.WriteFile(ordinary, []byte("ordinary\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, err = guard.validate(filepath.Base(ordinary), true); err != nil {
		t.Fatalf("disjoint protected root denied ordinary checkout file: %v", err)
	}

	hardlink := filepath.Join(root, "auth-hardlink.db")
	if err := os.Link(disjointProtected, hardlink); err == nil {
		if _, err = guard.validate(filepath.Base(hardlink), true); err == nil {
			t.Fatal("local repair accepted a hardlink alias of protected database")
		}
	}
}

func TestLocalRepairIdentityCatalogDeniesAliasAfterArchiveRename(t *testing.T) {
	pin, workspace, checkout := newLocalRepairTestWorkspace(t)
	activeRoot := filepath.Join(t.TempDir(), "workflow_runs")
	source := filepath.Join(activeRoot, "wr_fixture", "run.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := tools.NewFileIdentityCatalog(tools.FileIdentityCatalogOptions{
		TreeRoots: []string{activeRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(checkout, "runtime-alias.json")
	if linkErr := os.Link(source, alias); linkErr != nil {
		t.Skipf("hardlinks unavailable: %v", linkErr)
	}
	archive := filepath.Join(filepath.Dir(activeRoot), "legacy-json", "workflows-v1", "run.json")
	if mkdirErr := os.MkdirAll(filepath.Dir(archive), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if renameErr := os.Rename(source, archive); renameErr != nil {
		t.Fatal(renameErr)
	}
	guard, err := newLocalRepairPathGuardWithPolicy(workspace, pin, nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.validate(filepath.Base(alias), false); err == nil {
		t.Fatal("local repair read accepted an archived runtime hardlink alias")
	}
	if _, err := guard.validate(filepath.Base(alias), true); err == nil {
		t.Fatal("local repair mutation accepted an archived runtime hardlink alias")
	}
	ordinary := filepath.Join(checkout, "ordinary.txt")
	if err := os.WriteFile(ordinary, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.validate(filepath.Base(ordinary), true); err != nil {
		t.Fatalf("local repair denied ordinary file: %v", err)
	}
}

func TestLocalRepairProtectedRootValidationAndDrift(t *testing.T) {
	for _, configured := range [][]string{
		{""},
		{" protected "},
		{"invalid\x00root"},
	} {
		if roots, err := prepareLocalRepairProtectedRoots(configured); err == nil || roots != nil {
			t.Fatalf("invalid protected roots %q = %#v, %v", configured, roots, err)
		}
	}
	directory := t.TempDir()
	blocker := filepath.Join(directory, "blocker")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if roots, err := prepareLocalRepairProtectedRoots(
		[]string{filepath.Join(blocker, "child")},
	); err == nil || roots != nil {
		t.Fatalf("unresolvable protected roots = %#v, %v", roots, err)
	}

	pin, workspace, root := newLocalRepairTestWorkspace(t)
	protectedParent := t.TempDir()
	first := filepath.Join(protectedParent, "first")
	second := filepath.Join(protectedParent, "second")
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	protectedAlias := filepath.Join(protectedParent, "alias")
	if err := os.Symlink(first, protectedAlias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	guard, err := newLocalRepairPathGuard(workspace, pin, []string{protectedAlias})
	if err != nil {
		t.Fatal(err)
	}
	ordinary := filepath.Join(root, "ordinary.txt")
	if writeErr := os.WriteFile(ordinary, []byte("ordinary\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if removeErr := os.Remove(protectedAlias); removeErr != nil {
		t.Fatal(removeErr)
	}
	if linkErr := os.Symlink(second, protectedAlias); linkErr != nil {
		t.Fatal(linkErr)
	}
	if _, validateErr := guard.validate(filepath.Base(ordinary), true); validateErr == nil {
		t.Fatal("local repair accepted protected-root drift")
	}

	missingProtected := filepath.Join(protectedParent, "future.db")
	guard, err = newLocalRepairPathGuard(workspace, pin, []string{missingProtected})
	if err != nil {
		t.Fatal(err)
	}
	if denied, protectedErr := guard.protected(ordinary, ordinary); protectedErr != nil || denied {
		t.Fatalf("missing disjoint protected root denied ordinary = %t, %v", denied, protectedErr)
	}
}

func TestLocalRepairProtectedPathGuardCoverageMargin(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	if guard, err := newLocalRepairPathGuard(
		workspace,
		pin,
		[]string{"invalid\x00root"},
	); err == nil || guard != nil {
		t.Fatalf("invalid protected guard = %#v, %v", guard, err)
	}

	guard, err := newLocalRepairPathGuard(workspace, pin)
	if err != nil {
		t.Fatal(err)
	}
	if _, validateErr := (*localRepairPathGuard)(nil).validate("ordinary", true); validateErr == nil {
		t.Fatal("nil local-repair guard was accepted")
	}
	if _, validateErr := guard.validate("", true); validateErr == nil {
		t.Fatal("empty local-repair path was accepted")
	}

	blocker := filepath.Join(root, "blocker")
	if writeErr := os.WriteFile(blocker, []byte("blocker"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, validateErr := guard.validate(
		filepath.Join("blocker", "child"),
		true,
	); validateErr == nil {
		t.Fatal("local-repair path below a file was accepted")
	}
	directory := filepath.Join(root, "directory")
	if mkdirErr := os.Mkdir(directory, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if _, validateErr := guard.validate(filepath.Base(directory), true); validateErr == nil {
		t.Fatal("local-repair directory was accepted as an editable file")
	}
	oversized := filepath.Join(root, "oversized")
	oversizedFile, err := os.OpenFile(oversized, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = oversizedFile.Truncate(maxLocalRepairEditableFile + 1); err != nil {
		_ = oversizedFile.Close()
		t.Fatal(err)
	}
	if err = oversizedFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.validate(filepath.Base(oversized), true); err == nil {
		t.Fatal("oversized local-repair file was accepted")
	}

	protected := filepath.Join(t.TempDir(), "launcher-auth.db")
	if err := os.WriteFile(protected, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := &localRepairPathGuard{protectedRoots: []localRepairProtectedRoot{{
		lexical: protected, canonical: protected,
	}}}
	if denied, err := direct.protected(protected, protected); err != nil || !denied {
		t.Fatalf("direct protected path = %t, %v", denied, err)
	}
	badCandidate := filepath.Join(blocker, "child")
	if denied, err := direct.protected(badCandidate, badCandidate); err == nil || denied {
		t.Fatalf("invalid protected candidate = %t, %v", denied, err)
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
