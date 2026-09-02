package fstools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type fileMutationExecutable interface {
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

type fileMutationFaultFile struct {
	writeErr error
	syncErr  error
	closeErr error
}

func (file *fileMutationFaultFile) Write(data []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *fileMutationFaultFile) Sync() error  { return file.syncErr }
func (file *fileMutationFaultFile) Close() error { return file.closeErr }

func buildFileMutationTestTools(
	t *testing.T,
	workspace string,
	restrict bool,
	policy FileMutationPolicy,
	allowPaths ...[]*regexp.Regexp,
) map[string]fileMutationExecutable {
	t.Helper()
	writeTool, err := NewWriteFileToolWithPolicy(
		workspace, restrict, policy, allowPaths...,
	)
	if err != nil {
		t.Fatalf("NewWriteFileToolWithPolicy() error = %v", err)
	}
	editTool, err := NewEditFileToolWithPolicy(
		workspace, restrict, policy, allowPaths...,
	)
	if err != nil {
		t.Fatalf("NewEditFileToolWithPolicy() error = %v", err)
	}
	appendTool, err := NewAppendFileToolWithPolicy(
		workspace, restrict, policy, allowPaths...,
	)
	if err != nil {
		t.Fatalf("NewAppendFileToolWithPolicy() error = %v", err)
	}
	return map[string]fileMutationExecutable{
		"write_file":  writeTool,
		"edit_file":   editTool,
		"append_file": appendTool,
	}
}

func executeFileMutationTestTool(
	toolName string,
	tool fileMutationExecutable,
	path string,
) *ToolResult {
	args := map[string]any{"path": path}
	switch toolName {
	case "write_file":
		args["content"] = "changed"
		args["overwrite"] = true
	case "edit_file":
		args["old_text"] = "before"
		args["new_text"] = "changed"
	case "append_file":
		args["content"] = "changed"
	}
	return tool.Execute(context.Background(), args)
}

func requireFileMutationPolicyDenied(
	t *testing.T,
	toolName string,
	tool fileMutationExecutable,
	path string,
) {
	t.Helper()
	result := executeFileMutationTestTool(toolName, tool, path)
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, "protected runtime state") {
		t.Fatalf("%s protected result = %#v", toolName, result)
	}
	if strings.Contains(result.ForLLM, filepath.Clean(path)) {
		t.Fatalf("%s error disclosed protected path: %q", toolName, result.ForLLM)
	}
}

func TestFileMutationPolicyProtectsPresentAndAbsentRuntimePaths(t *testing.T) {
	workspace := t.TempDir()
	database := filepath.Join(workspace, "launcher-auth.db")
	wal := database + "-wal"
	shm := database + "-shm"
	archiveRoot := filepath.Join(workspace, "legacy-json")
	archive := filepath.Join(archiveRoot, "launcher-auth-v1", "launcher-config.json")
	for _, path := range []string{database, shm, archive} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tools := buildFileMutationTestTools(t, workspace, true, FileMutationPolicy{
		ProtectedRoots: []string{database, wal, shm, archiveRoot, archive},
	})

	for toolName, tool := range tools {
		for _, path := range []string{database, wal, shm, archive} {
			t.Run(toolName+"_"+filepath.Base(path), func(t *testing.T) {
				requireFileMutationPolicyDenied(t, toolName, tool, path)
			})
		}
	}

	for _, path := range []string{database, shm, archive} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != "before" {
			t.Fatalf("protected content %q = %q, %v", path, content, err)
		}
	}
	if _, err := os.Stat(wal); !os.IsNotExist(err) {
		t.Fatalf("absent WAL was created: %v", err)
	}
}

func TestPreparedFileMutationPolicyIsDetachedAndSharedByToolFilesystems(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "runtime.db")
	if err := os.WriteFile(protected, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := []string{protected}
	prepared, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedRoots: roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	roots[0] = filepath.Join(workspace, "ordinary.txt")
	first, err := buildMutationFS(
		workspace,
		false,
		nil,
		FileMutationPolicy{Prepared: prepared},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildMutationFS(
		workspace,
		false,
		nil,
		FileMutationPolicy{Prepared: prepared},
	)
	if err != nil {
		t.Fatal(err)
	}
	firstProtected, firstOK := first.(*protectedMutationFS)
	secondProtected, secondOK := second.(*protectedMutationFS)
	if !firstOK || !secondOK || len(firstProtected.roots) != 1 ||
		len(secondProtected.roots) != 1 || &firstProtected.roots[0] != &secondProtected.roots[0] {
		t.Fatalf("prepared roots were copied: %#v / %#v", first, second)
	}
	if err := first.WriteFile(protected, []byte("changed")); err == nil {
		t.Fatal("detached prepared policy accepted protected mutation")
	}
	if mixed, mixedErr := buildMutationFS(workspace, false, nil, FileMutationPolicy{
		Prepared:       prepared,
		ProtectedRoots: []string{protected},
	}); mixedErr == nil || mixed != nil {
		t.Fatalf("mixed prepared/source policy = %#v, %v", mixed, mixedErr)
	}
	if mixed, mixedErr := buildMutationFS(workspace, false, nil, FileMutationPolicy{
		Prepared: prepared,
		ProtectedSiblingPrefixes: []FileMutationSiblingPrefix{{
			Parent: workspace, Prefix: "runtime.",
		}},
	}); mixedErr == nil || mixed != nil {
		t.Fatalf("mixed prepared/sibling-prefix policy = %#v, %v", mixed, mixedErr)
	}
	if preparedAgain, preparedErr := NewPreparedFileMutationPolicy(
		workspace,
		FileMutationPolicy{Prepared: prepared},
	); preparedErr == nil || preparedAgain != nil {
		t.Fatalf("reprepared policy = %#v, %v", preparedAgain, preparedErr)
	}
}

func TestFileMutationPolicyProtectsDynamicSiblingPrefixes(t *testing.T) {
	workspace := t.TempDir()
	prefixes := []FileMutationSiblingPrefix{{
		Parent: workspace,
		Prefix: "account_router_state.json.auth-invalidation.",
	}}
	prepared, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedSiblingPrefixes: prefixes,
	})
	if err != nil {
		t.Fatal(err)
	}
	prefixes[0].Prefix = "ordinary."
	target := filepath.Join(
		workspace,
		"account_router_state.json.auth-invalidation.0123456789abcdef0123456789abcdef",
	)
	for _, policy := range []FileMutationPolicy{
		{ProtectedSiblingPrefixes: []FileMutationSiblingPrefix{{
			Parent: workspace, Prefix: "account_router_state.json.auth-invalidation.",
		}}},
		{Prepared: prepared},
	} {
		for toolName, tool := range buildFileMutationTestTools(t, workspace, true, policy) {
			requireFileMutationPolicyDenied(t, toolName, tool, target)
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("protected sibling was created: %v", err)
	}
	ordinary := filepath.Join(workspace, "account_router_state.json")
	tool := buildFileMutationTestTools(
		t,
		workspace,
		true,
		FileMutationPolicy{Prepared: prepared},
	)["write_file"]
	result := executeFileMutationTestTool("write_file", tool, ordinary)
	if result == nil || result.IsError {
		t.Fatalf("adjacent ordinary file was denied: %#v", result)
	}
	if protected, err := prepared.ProtectsPath(target); err != nil || !protected {
		t.Fatalf("prepared sibling prefix protected=%t err=%v", protected, err)
	}
	if overlap, err := prepared.OverlapsPath(workspace); err != nil || !overlap {
		t.Fatalf("sibling-prefix parent overlap=%t err=%v", overlap, err)
	}
	for _, invalid := range []FileMutationSiblingPrefix{
		{Parent: "relative", Prefix: "sidecar."},
		{Parent: workspace, Prefix: "../sidecar."},
		{Parent: workspace, Prefix: ""},
	} {
		if candidate, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
			ProtectedSiblingPrefixes: []FileMutationSiblingPrefix{invalid},
		}); err == nil || candidate != nil {
			t.Fatalf("invalid sibling prefix %#v accepted: %#v, %v", invalid, candidate, err)
		}
	}
}

func TestFileMutationSiblingPrefixParentDriftFailsClosed(t *testing.T) {
	workspace := t.TempDir()
	parent := filepath.Join(workspace, "runtime")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	prepared, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedSiblingPrefixes: []FileMutationSiblingPrefix{{
			Parent: parent, Prefix: "sidecar.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	moved := parent + "-moved"
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, parent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	target := filepath.Join(workspace, "ordinary.txt")
	tool := buildFileMutationTestTools(
		t,
		workspace,
		true,
		FileMutationPolicy{Prepared: prepared},
	)["write_file"]
	requireFileMutationPolicyDenied(t, "write_file", tool, target)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("parent drift allowed an ordinary mutation: %v", err)
	}
}

func TestFileMutationPolicyProtectsMissingArchiveNamespace(t *testing.T) {
	workspace := t.TempDir()
	archiveRoot := filepath.Join(workspace, "legacy-json")
	target := filepath.Join(archiveRoot, "launcher-auth-v1", "launcher-config.json")
	tools := buildFileMutationTestTools(t, workspace, true, FileMutationPolicy{
		ProtectedRoots: []string{archiveRoot},
	})
	for toolName, tool := range tools {
		requireFileMutationPolicyDenied(t, toolName, tool, target)
	}
	if _, err := os.Stat(archiveRoot); !os.IsNotExist(err) {
		t.Fatalf("protected archive namespace was created: %v", err)
	}
}

func TestFileMutationPolicyLeavesAdjacentConfigAndSourceEditable(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "launcher-auth.db")
	tools := buildFileMutationTestTools(t, workspace, true, FileMutationPolicy{
		ProtectedRoots: []string{protected},
	})

	for toolName, tool := range tools {
		t.Run(toolName, func(t *testing.T) {
			target := filepath.Join(workspace, toolName+"-config.json")
			if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			result := executeFileMutationTestTool(toolName, tool, target)
			if result == nil || result.IsError {
				t.Fatalf("ordinary mutation result = %#v", result)
			}
			content, err := os.ReadFile(target)
			want := "changed"
			if toolName == "append_file" {
				want = "beforechanged"
			}
			if err != nil || string(content) != want {
				t.Fatalf("ordinary content = %q, %v", content, err)
			}
		})
	}
}

func TestFileMutationPolicyOverridesAliasesAndAllowPaths(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	protected := filepath.Join(outside, "launcher-auth.db")
	if err := os.WriteFile(protected, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	patterns := []*regexp.Regexp{regexp.MustCompile(
		"^" + regexp.QuoteMeta(filepath.Clean(outside)) + "(?:" +
			regexp.QuoteMeta(string(os.PathSeparator)) + "|$)",
	)}
	policy := FileMutationPolicy{ProtectedRoots: []string{protected}}
	tools := buildFileMutationTestTools(t, workspace, true, policy, patterns)
	for toolName, tool := range tools {
		requireFileMutationPolicyDenied(t, toolName, tool, protected)
	}

	ordinary := filepath.Join(outside, "ordinary.txt")
	writeTool := tools["write_file"]
	result := executeFileMutationTestTool("write_file", writeTool, ordinary)
	if result == nil || result.IsError {
		t.Fatalf("allowed ordinary outside write = %#v", result)
	}

	t.Run("symlink", func(t *testing.T) {
		alias := filepath.Join(workspace, "auth-alias.db")
		if err := os.Symlink(protected, alias); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		aliasTools := buildFileMutationTestTools(t, workspace, true, policy)
		for toolName, tool := range aliasTools {
			requireFileMutationPolicyDenied(t, toolName, tool, alias)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		alias := filepath.Join(workspace, "auth-hardlink.db")
		if err := os.Link(protected, alias); err != nil {
			t.Skipf("hardlink unavailable: %v", err)
		}
		aliasTools := buildFileMutationTestTools(t, workspace, true, policy)
		for toolName, tool := range aliasTools {
			requireFileMutationPolicyDenied(t, toolName, tool, alias)
		}
	})

	t.Run("archive hardlink", func(t *testing.T) {
		archiveRoot := filepath.Join(workspace, "legacy-json")
		archive := filepath.Join(
			archiveRoot, "launcher-auth-v1", "launcher-config.json",
		)
		if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(workspace, "archived-credentials-hardlink.json")
		if err := os.Link(archive, alias); err != nil {
			t.Skipf("hardlink unavailable: %v", err)
		}
		archiveTools := buildFileMutationTestTools(t, workspace, true, FileMutationPolicy{
			ProtectedRoots: []string{archiveRoot, archive},
		})
		for toolName, tool := range archiveTools {
			requireFileMutationPolicyDenied(t, toolName, tool, alias)
		}
	})
}

func TestFileMutationPolicyUnrestrictedRelativePathsUseWorkingDirectory(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	protected := filepath.Join(workingDirectory, "launcher-auth.db")
	if err := os.WriteFile(protected, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := buildFileMutationTestTools(t, t.TempDir(), false, FileMutationPolicy{
		ProtectedRoots: []string{protected},
	})
	for toolName, tool := range tools {
		requireFileMutationPolicyDenied(t, toolName, tool, filepath.Base(protected))
	}
}

func TestFileMutationPolicyRejectsParentSymlinkRetargetAfterPin(t *testing.T) {
	workspace := t.TempDir()
	safeParent := filepath.Join(workspace, "safe")
	protectedParent := filepath.Join(workspace, "protected")
	for _, directory := range []string{safeParent, protectedParent} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	protected := filepath.Join(protectedParent, "launcher-auth.db")
	if err := os.WriteFile(protected, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(workspace, "alias")
	if err := os.Symlink(safeParent, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tool, err := NewWriteFileToolWithPolicy(workspace, false, FileMutationPolicy{
		ProtectedRoots: []string{protected},
	})
	if err != nil {
		t.Fatal(err)
	}
	guarded, ok := tool.fs.(*protectedMutationFS)
	if !ok {
		t.Fatalf("write tool filesystem = %T", tool.fs)
	}
	guarded.beforePinnedWrite = func() {
		if removeErr := os.Remove(alias); removeErr != nil {
			t.Fatal(removeErr)
		}
		if linkErr := os.Symlink(protectedParent, alias); linkErr != nil {
			t.Fatal(linkErr)
		}
	}

	requireFileMutationPolicyDenied(
		t,
		"write_file",
		tool,
		filepath.Join(alias, filepath.Base(protected)),
	)
	content, err := os.ReadFile(protected)
	if err != nil || string(content) != "before" {
		t.Fatalf("protected content after retarget = %q, %v", content, err)
	}
	if _, err = os.Stat(filepath.Join(safeParent, filepath.Base(protected))); !os.IsNotExist(err) {
		t.Fatalf("pinned safe parent was unexpectedly written: %v", err)
	}
}

func TestFileMutationPolicyDetachesInputsAndFailsClosedOnRootDrift(t *testing.T) {
	workspace := t.TempDir()
	rootParent := filepath.Join(workspace, "runtime")
	protected := filepath.Join(rootParent, "launcher-auth.db")
	if err := os.MkdirAll(rootParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protected, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := []string{protected}
	tool, err := NewWriteFileToolWithPolicy(workspace, true, FileMutationPolicy{
		ProtectedRoots: roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	roots[0] = filepath.Join(workspace, "ordinary.txt")
	requireFileMutationPolicyDenied(t, "write_file", tool, protected)

	movedParent := rootParent + "-moved"
	if err := os.Rename(rootParent, movedParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(movedParent, rootParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ordinary := filepath.Join(workspace, "still-ordinary.txt")
	requireFileMutationPolicyDenied(t, "write_file", tool, ordinary)
	if _, err := os.Stat(ordinary); !os.IsNotExist(err) {
		t.Fatalf("write after root drift was not fail-closed: %v", err)
	}
}

func TestFileMutationPolicyRejectsInvalidRoots(t *testing.T) {
	for _, root := range []string{"", " protected ", "invalid\x00root"} {
		if tool, err := NewWriteFileToolWithPolicy(
			t.TempDir(),
			true,
			FileMutationPolicy{ProtectedRoots: []string{root}},
		); err == nil || tool != nil {
			t.Fatalf("invalid protected root %q returned %#v, %v", root, tool, err)
		}
		if tool, err := NewEditFileToolWithPolicy(
			t.TempDir(),
			true,
			FileMutationPolicy{ProtectedRoots: []string{root}},
		); err == nil || tool != nil {
			t.Fatalf("invalid edit protected root %q returned %#v, %v", root, tool, err)
		}
		if tool, err := NewAppendFileToolWithPolicy(
			t.TempDir(),
			true,
			FileMutationPolicy{ProtectedRoots: []string{root}},
		); err == nil || tool != nil {
			t.Fatalf("invalid append protected root %q returned %#v, %v", root, tool, err)
		}
	}
}

func TestFileMutationPolicyInternalAccessAndPreparationEdges(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "launcher-auth.db")
	ordinary := filepath.Join(workspace, "ordinary.txt")
	if err := os.WriteFile(protected, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ordinary, []byte("ordinary"), 0o600); err != nil {
		t.Fatal(err)
	}
	policyFS, err := buildMutationFS(workspace, true, nil, FileMutationPolicy{
		ProtectedRoots: []string{protected},
	})
	if err != nil {
		t.Fatal(err)
	}
	guarded := policyFS.(*protectedMutationFS)
	if entries, readErr := guarded.ReadDir(workspace); readErr != nil || len(entries) < 2 {
		t.Fatalf("ReadDir ordinary root = %#v, %v", entries, readErr)
	}
	opened, openErr := guarded.Open(ordinary)
	if openErr != nil {
		t.Fatal(openErr)
	}
	_ = opened.Close()
	if _, readErr := guarded.ReadDir(protected); readErr == nil {
		t.Fatal("ReadDir accepted protected root")
	}
	if opened, openErr = guarded.Open(protected); openErr == nil || opened != nil {
		t.Fatalf("Open protected root = %#v, %v", opened, openErr)
	}
	if unguarded, buildErr := buildMutationFS(
		workspace, true, nil, FileMutationPolicy{},
	); buildErr != nil || unguarded == nil {
		t.Fatalf("empty mutation policy = %T, %v", unguarded, buildErr)
	}

	relativeRoots, err := prepareFileMutationProtectedRoots(
		workspace,
		[]string{"relative-runtime", "relative-runtime"},
	)
	if err != nil || len(relativeRoots) != 1 {
		t.Fatalf("relative duplicate roots = %#v, %v", relativeRoots, err)
	}
	if roots, err := prepareFileMutationProtectedRoots(workspace, nil); err != nil || roots != nil {
		t.Fatalf("empty protected roots = %#v, %v", roots, err)
	}
	blocker := filepath.Join(workspace, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if roots, err := prepareFileMutationProtectedRoots(
		workspace,
		[]string{filepath.Join(blocker, "child")},
	); err == nil || roots != nil {
		t.Fatalf("unresolvable protected root = %#v, %v", roots, err)
	}
	if err := guarded.WriteFile(filepath.Join(blocker, "child"), []byte("denied")); err == nil {
		t.Fatal("WriteFile accepted an unresolvable candidate")
	}

	hostGuard := &protectedMutationFS{restrict: false}
	if err := hostGuard.prepareMutationParent(filepath.Join(blocker, "child", "file")); err == nil {
		t.Fatal("host parent preparation accepted a non-directory ancestor")
	}
	absolute, absErr := fileMutationAbsolutePath("", true, "relative.txt")
	if absErr != nil || !filepath.IsAbs(absolute) {
		t.Fatalf("empty-workspace relative path = %q, %v", absolute, absErr)
	}
}

func TestFileMutationPolicyBindsReadsToOpenedHandleIdentity(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(t.TempDir(), "runtime-secret.json")
	if err := os.WriteFile(protected, []byte("never disclose this runtime secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{protected},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, operation := range []string{"read", "open", "edit", "append"} {
		t.Run(operation, func(t *testing.T) {
			ordinary := filepath.Join(workspace, operation+".txt")
			if err := os.WriteFile(ordinary, []byte("ordinary"), 0o600); err != nil {
				t.Fatal(err)
			}
			policyFS, err := buildMutationFS(workspace, true, nil, FileMutationPolicy{
				ProtectedIdentities: catalog,
			})
			if err != nil {
				t.Fatal(err)
			}
			guarded := policyFS.(*protectedMutationFS)
			guarded.beforeProtectedOpen = func(string) {
				guarded.beforeProtectedOpen = nil
				if removeErr := os.Remove(ordinary); removeErr != nil {
					t.Fatal(removeErr)
				}
				if linkErr := os.Link(protected, ordinary); linkErr != nil {
					t.Fatal(linkErr)
				}
			}

			var result *ToolResult
			switch operation {
			case "read":
				content, readErr := guarded.ReadFile(filepath.Base(ordinary))
				if readErr == nil || content != nil || strings.Contains(string(content), "secret") {
					t.Fatalf("swap-raced read = %q, %v", content, readErr)
				}
			case "open":
				opened, openErr := guarded.Open(filepath.Base(ordinary))
				if openErr == nil || opened != nil {
					t.Fatalf("swap-raced open = %#v, %v", opened, openErr)
				}
			case "edit":
				result = (&EditFileTool{fs: guarded}).Execute(context.Background(), map[string]any{
					"path": filepath.Base(ordinary), "old_text": "ordinary", "new_text": "changed",
				})
			case "append":
				result = (&AppendFileTool{fs: guarded}).Execute(context.Background(), map[string]any{
					"path": filepath.Base(ordinary), "content": "changed",
				})
			}
			if result != nil && (!result.IsError ||
				strings.Contains(result.ForLLM, "runtime secret") ||
				strings.Contains(result.ForUser, "runtime secret")) {
				t.Fatalf("swap-raced %s result = %#v", operation, result)
			}
			content, readErr := os.ReadFile(protected)
			if readErr != nil || string(content) != "never disclose this runtime secret" {
				t.Fatalf("protected bytes after %s = %q, %v", operation, content, readErr)
			}
		})
	}
}

func TestFileMutationPolicyBindsDirectoryListingToOpenedHandle(t *testing.T) {
	workspace := t.TempDir()
	safe := filepath.Join(workspace, "safe")
	protected := filepath.Join(workspace, "runtime")
	for _, directory := range []string{safe, protected} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const privateName = "private-account-identity.json"
	if err := os.WriteFile(filepath.Join(protected, privateName), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	policyFS, err := buildMutationFS(workspace, false, nil, FileMutationPolicy{
		ProtectedRoots: []string{protected},
	})
	if err != nil {
		t.Fatal(err)
	}
	guarded := policyFS.(*protectedMutationFS)
	guarded.beforeProtectedOpen = func(string) {
		guarded.beforeProtectedOpen = nil
		if renameErr := os.Rename(safe, safe+"-original"); renameErr != nil {
			t.Fatal(renameErr)
		}
		if linkErr := os.Symlink(protected, safe); linkErr != nil {
			t.Skipf("symlinks unavailable: %v", linkErr)
		}
	}
	entries, readErr := guarded.ReadDir(safe)
	if readErr == nil || entries != nil {
		t.Fatalf("swap-raced directory listing = %#v, %v", entries, readErr)
	}
	if strings.Contains(readErr.Error(), privateName) {
		t.Fatalf("directory rejection disclosed protected entry: %v", readErr)
	}
}

func TestFileMutationPolicyPinnedWriterEdges(t *testing.T) {
	if err := writeFileInPinnedRoot(nil, "target", nil); err == nil {
		t.Fatal("nil pinned root accepted")
	}
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeFileInPinnedRoot(root, filepath.Join("nested", "target"), nil); err == nil {
		t.Fatal("non-base pinned target accepted")
	}
	if err = os.Mkdir(filepath.Join(directory, "directory-target"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = writeFileInPinnedRoot(root, "directory-target", []byte("content")); err == nil {
		t.Fatal("pinned writer replaced a directory")
	}
	if closeErr := root.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err = writeFileInPinnedRoot(root, "closed-target", nil); err == nil {
		t.Fatal("closed pinned root accepted")
	}
}

func TestFileMutationPolicyPinnedWriterFileFaults(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fault := func(file *fileMutationFaultFile) error {
		return writeFileInPinnedRootWithOpen(
			root,
			"target",
			[]byte("content"),
			func(string, int, os.FileMode) (fileMutationTemporaryFile, error) {
				return file, nil
			},
		)
	}
	if err = writeFileInPinnedRootWithOpen(
		root,
		"target",
		nil,
		func(string, int, os.FileMode) (fileMutationTemporaryFile, error) {
			return nil, os.ErrPermission
		},
	); err == nil {
		t.Fatal("pinned writer accepted open fault")
	}
	if err = fault(&fileMutationFaultFile{writeErr: os.ErrPermission}); err == nil {
		t.Fatal("pinned writer accepted write fault")
	}
	if err = fault(&fileMutationFaultFile{syncErr: os.ErrPermission}); err == nil {
		t.Fatal("pinned writer accepted sync fault")
	}
	if err = fault(&fileMutationFaultFile{closeErr: os.ErrPermission}); err == nil {
		t.Fatal("pinned writer accepted close fault")
	}
}

func TestFileMutationPolicyRejectsUnprotectedParentRetarget(t *testing.T) {
	workspace := t.TempDir()
	first := filepath.Join(workspace, "first")
	second := filepath.Join(workspace, "second")
	protected := filepath.Join(workspace, "launcher-auth.db")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(protected, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(workspace, "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tool, err := NewWriteFileToolWithPolicy(workspace, false, FileMutationPolicy{
		ProtectedRoots: []string{protected},
	})
	if err != nil {
		t.Fatal(err)
	}
	guarded := tool.fs.(*protectedMutationFS)
	guarded.beforePinnedWrite = func() {
		if removeErr := os.Remove(alias); removeErr != nil {
			t.Fatal(removeErr)
		}
		if linkErr := os.Symlink(second, alias); linkErr != nil {
			t.Fatal(linkErr)
		}
	}
	result := tool.Execute(context.Background(), map[string]any{
		"path": filepath.Join(alias, "ordinary.txt"), "content": "changed",
	})
	if result == nil || !result.IsError {
		t.Fatalf("retargeted unprotected parent result = %#v", result)
	}
	for _, parent := range []string{first, second} {
		if _, err := os.Stat(filepath.Join(parent, "ordinary.txt")); !os.IsNotExist(err) {
			t.Fatalf("retarget wrote through %q: %v", parent, err)
		}
	}
}

func TestFileMutationPolicyRejectsAuthorityAndPermissionFailures(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "launcher-auth.db")
	if err := os.WriteFile(protected, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, err := NewWriteFileToolWithPolicy(workspace, true, FileMutationPolicy{
		ProtectedRoots: []string{protected},
	})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	result := tool.Execute(context.Background(), map[string]any{
		"path": outside, "content": "denied",
	})
	if result == nil || !result.IsError {
		t.Fatalf("outside write result = %#v", result)
	}

	if os.PathSeparator == '\\' {
		return
	}
	locked := filepath.Join(workspace, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o700)
	result = tool.Execute(context.Background(), map[string]any{
		"path": filepath.Join(locked, "nested", "target"), "content": "denied",
	})
	if result == nil || !result.IsError {
		t.Fatalf("locked-parent write result = %#v", result)
	}
}

func TestFileMutationPolicyRechecksAuthorityAfterParentPin(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "launcher-auth.db")
	if err := os.WriteFile(protected, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(workspace, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	tool, err := NewWriteFileToolWithPolicy(workspace, true, FileMutationPolicy{
		ProtectedRoots: []string{protected},
	})
	if err != nil {
		t.Fatal(err)
	}
	guarded := tool.fs.(*protectedMutationFS)
	outside := filepath.Join(t.TempDir(), "moved-parent")
	guarded.beforePinnedWrite = func() {
		if renameErr := os.Rename(parent, outside); renameErr != nil {
			t.Fatal(renameErr)
		}
		if linkErr := os.Symlink(outside, parent); linkErr != nil {
			t.Fatal(linkErr)
		}
	}
	result := tool.Execute(context.Background(), map[string]any{
		"path": filepath.Join(parent, "ordinary.txt"), "content": "denied",
	})
	if result == nil || !result.IsError {
		t.Fatalf("post-pin authority result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "ordinary.txt")); !os.IsNotExist(err) {
		t.Fatalf("post-pin authority wrote outside workspace: %v", err)
	}
}

func TestFileMutationPolicySameFileErrorEdges(t *testing.T) {
	directory := t.TempDir()
	existing := filepath.Join(directory, "existing")
	if err := os.WriteFile(existing, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(directory, "missing")
	if same, err := fileMutationSameExistingFile(existing, missing); err != nil || same {
		t.Fatalf("existing/missing SameFile = %t, %v", same, err)
	}
	blocker := filepath.Join(directory, "blocker")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "child")
	if same, err := fileMutationSameExistingFile(bad, existing); err == nil || same {
		t.Fatalf("invalid-left SameFile = %t, %v", same, err)
	}
	if same, err := fileMutationSameExistingFile(existing, bad); err == nil || same {
		t.Fatalf("invalid-right SameFile = %t, %v", same, err)
	}
}

func TestFileMutationPolicyAbsolutePathFailsWithoutWorkingDirectory(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows cannot remove the process working directory")
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	unavailable := t.TempDir()
	if err = os.Chdir(unavailable); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if restoreErr := os.Chdir(original); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	}()
	if err = os.Remove(unavailable); err != nil {
		t.Fatal(err)
	}
	if roots, rootErr := prepareFileMutationProtectedRoots("", []string{"relative"}); rootErr == nil || roots != nil {
		t.Fatalf("roots without cwd = %#v, %v", roots, rootErr)
	}
	if absolute, absErr := fileMutationAbsolutePath("", false, "relative"); absErr == nil || absolute != "" {
		t.Fatalf("absolute path without cwd = %q, %v", absolute, absErr)
	}
}

func TestFileMutationPolicyInjectedPathFailures(t *testing.T) {
	canary := errors.New("mutation path canary")
	originalAbs := fileMutationAbs
	originalEval := fileMutationEvalSymlinks
	originalOpenRoot := fileMutationOpenRoot
	originalMkdirAll := fileMutationMkdirAll
	originalSafeRelative := fileMutationSafeRelativePath
	originalValidatePlatform := fileMutationValidatePlatform
	reset := func() {
		fileMutationAbs = originalAbs
		fileMutationEvalSymlinks = originalEval
		fileMutationOpenRoot = originalOpenRoot
		fileMutationMkdirAll = originalMkdirAll
		fileMutationSafeRelativePath = originalSafeRelative
		fileMutationValidatePlatform = originalValidatePlatform
	}
	t.Cleanup(reset)

	t.Run("protected root absolute", func(t *testing.T) {
		reset()
		fileMutationAbs = func(string) (string, error) { return "", canary }
		if roots, err := prepareFileMutationProtectedRoots(".", []string{"protected"}); err == nil || roots != nil {
			t.Fatalf("prepare roots = %#v, %v", roots, err)
		}
	})
	t.Run("protected root second absolute", func(t *testing.T) {
		reset()
		calls := 0
		fileMutationAbs = func(path string) (string, error) {
			calls++
			if calls == 2 {
				return "", canary
			}
			return originalAbs(path)
		}
		if roots, err := prepareFileMutationProtectedRoots(
			t.TempDir(),
			[]string{"protected"},
		); err == nil || roots != nil {
			t.Fatalf("prepare roots after second Abs failure = %#v, %v", roots, err)
		}
	})
	t.Run("protected root platform", func(t *testing.T) {
		reset()
		fileMutationValidatePlatform = func(string) error { return canary }
		if roots, err := prepareFileMutationProtectedRoots(
			t.TempDir(),
			[]string{"protected"},
		); err == nil ||
			roots != nil {
			t.Fatalf("prepare roots = %#v, %v", roots, err)
		}
	})
	t.Run("second candidate absolute", func(t *testing.T) {
		reset()
		calls := 0
		fileMutationAbs = func(path string) (string, error) {
			calls++
			if calls == 2 {
				return "", canary
			}
			return originalAbs(path)
		}
		guard := &protectedMutationFS{delegate: &hostFs{}}
		if err := guard.WriteFile("ordinary.txt", []byte("content")); err == nil {
			t.Fatal("WriteFile accepted a failed second absolute resolution")
		}
	})
	t.Run("parent resolution", func(t *testing.T) {
		reset()
		fileMutationEvalSymlinks = func(string) (string, error) { return "", canary }
		guard := &protectedMutationFS{delegate: &hostFs{}}
		if err := guard.WriteFile(filepath.Join(t.TempDir(), "ordinary.txt"), []byte("content")); err == nil {
			t.Fatal("WriteFile accepted an unresolved parent")
		}
	})
	t.Run("parent open", func(t *testing.T) {
		reset()
		fileMutationOpenRoot = func(string) (*os.Root, error) { return nil, canary }
		guard := &protectedMutationFS{delegate: &hostFs{}}
		if err := guard.WriteFile(
			filepath.Join(t.TempDir(), "ordinary.txt"),
			[]byte("content"),
		); !errors.Is(
			err,
			canary,
		) {
			t.Fatalf("WriteFile error = %v", err)
		}
	})
	t.Run("host parent create", func(t *testing.T) {
		reset()
		fileMutationMkdirAll = func(string, os.FileMode) error { return canary }
		guard := &protectedMutationFS{}
		if err := guard.prepareMutationParent(filepath.Join(t.TempDir(), "missing", "file")); !errors.Is(err, canary) {
			t.Fatalf("prepareMutationParent error = %v", err)
		}
	})
	t.Run("workspace absolute", func(t *testing.T) {
		reset()
		fileMutationAbs = func(string) (string, error) { return "", canary }
		guard := &protectedMutationFS{restrict: true, workspace: t.TempDir()}
		if err := guard.prepareMutationParent(filepath.Join(guard.workspace, "file")); !errors.Is(err, canary) {
			t.Fatalf("prepareMutationParent error = %v", err)
		}
	})
	t.Run("workspace open", func(t *testing.T) {
		reset()
		fileMutationOpenRoot = func(string) (*os.Root, error) { return nil, canary }
		guard := &protectedMutationFS{restrict: true, workspace: t.TempDir()}
		if err := guard.prepareMutationParent(filepath.Join(guard.workspace, "file")); !errors.Is(err, canary) {
			t.Fatalf("prepareMutationParent error = %v", err)
		}
	})
	t.Run("safe relative path", func(t *testing.T) {
		reset()
		fileMutationSafeRelativePath = func(string, string) (string, error) { return "", canary }
		guard := &protectedMutationFS{restrict: true, workspace: t.TempDir()}
		if err := guard.prepareMutationParent(filepath.Join(guard.workspace, "file")); !errors.Is(err, canary) {
			t.Fatalf("prepareMutationParent error = %v", err)
		}
	})
	t.Run("access absolute", func(t *testing.T) {
		reset()
		fileMutationAbs = func(string) (string, error) { return "", canary }
		if err := (&protectedMutationFS{}).validateAccess("relative"); err == nil {
			t.Fatal("validateAccess accepted unresolved path")
		}
	})
	t.Run("access platform", func(t *testing.T) {
		reset()
		fileMutationValidatePlatform = func(string) error { return canary }
		if err := (&protectedMutationFS{}).validateAccess(t.TempDir()); err == nil {
			t.Fatal("validateAccess accepted ambiguous platform path")
		}
	})
}
