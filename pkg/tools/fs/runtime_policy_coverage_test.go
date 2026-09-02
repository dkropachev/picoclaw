package fstools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPreparedFileMutationPolicyQueryMatrix(t *testing.T) {
	workspace := t.TempDir()
	protectedDir := filepath.Join(workspace, "runtime")
	protectedFile := filepath.Join(workspace, "runtime-root.db")
	identityFile := filepath.Join(workspace, "archived-state.json")
	ordinaryFile := filepath.Join(workspace, "ordinary.txt")
	for _, directory := range []string{protectedDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(protectedDir, "state.db"): "runtime",
		protectedFile:                           "root",
		identityFile:                            "archived",
		ordinaryFile:                            "ordinary",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	catalog, err := NewFileIdentityCatalog(FileIdentityCatalogOptions{
		ExactPaths: []string{identityFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedRoots:      []string{protectedDir, protectedFile},
		ProtectedIdentities: catalog,
		ProtectedSiblingPrefixes: []FileMutationSiblingPrefix{{
			Parent: workspace,
			Prefix: "runtime-sidecar.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		path string
		want bool
	}{
		"root":       {path: protectedDir, want: true},
		"descendant": {path: filepath.Join(protectedDir, "state.db"), want: true},
		"identity":   {path: identityFile, want: true},
		"sibling":    {path: filepath.Join(workspace, "runtime-sidecar.42"), want: true},
		"ordinary":   {path: ordinaryFile, want: false},
		"missing":    {path: filepath.Join(workspace, "missing.txt"), want: false},
	} {
		t.Run("protects-path-"+name, func(t *testing.T) {
			got, queryErr := prepared.ProtectsPath(test.path)
			if queryErr != nil || got != test.want {
				t.Fatalf("ProtectsPath() = %t, %v; want %t", got, queryErr, test.want)
			}
		})
	}
	alias := filepath.Join(workspace, "runtime-root.alias")
	if err := os.Link(protectedFile, alias); err == nil {
		if protected, queryErr := prepared.ProtectsPath(alias); queryErr != nil || !protected {
			t.Fatalf("hardlink-root ProtectsPath() = %t, %v", protected, queryErr)
		}
	}

	for name, test := range map[string]struct {
		path string
		want bool
	}{
		"ancestor":   {path: workspace, want: true},
		"root":       {path: protectedDir, want: true},
		"descendant": {path: filepath.Join(protectedDir, "child"), want: true},
		"sibling":    {path: filepath.Join(workspace, "runtime-sidecar.next"), want: true},
		"ordinary":   {path: ordinaryFile, want: false},
	} {
		t.Run("overlaps-path-"+name, func(t *testing.T) {
			got, queryErr := prepared.OverlapsPath(test.path)
			if queryErr != nil || got != test.want {
				t.Fatalf("OverlapsPath() = %t, %v; want %t", got, queryErr, test.want)
			}
		})
	}

	open := func(t *testing.T, path string) (*os.File, os.FileInfo) {
		t.Helper()
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file, info
	}

	identityHandle, identityInfo := open(t, identityFile)
	if protected, queryErr := prepared.ProtectsOpenedFile(identityHandle, identityInfo); queryErr != nil || !protected {
		t.Fatalf("identity ProtectsOpenedFile() = %t, %v", protected, queryErr)
	}
	rootHandle, rootInfo := open(t, protectedFile)
	if protected, queryErr := prepared.ProtectsOpenedFile(rootHandle, rootInfo); queryErr != nil || !protected {
		t.Fatalf("root ProtectsOpenedFile() = %t, %v", protected, queryErr)
	}
	ordinaryHandle, ordinaryInfo := open(t, ordinaryFile)
	if protected, queryErr := prepared.ProtectsOpenedFile(ordinaryHandle, ordinaryInfo); queryErr != nil || protected {
		t.Fatalf("ordinary ProtectsOpenedFile() = %t, %v", protected, queryErr)
	}
	descendantPath := filepath.Join(protectedDir, "state.db")
	descendantHandle, descendantInfo := open(t, descendantPath)
	if protected, queryErr := prepared.ProtectsOpenedPath(
		descendantPath,
		descendantPath,
		descendantHandle,
		descendantInfo,
	); queryErr != nil || !protected {
		t.Fatalf("descendant ProtectsOpenedPath() = %t, %v", protected, queryErr)
	}
	sidecarPath := filepath.Join(workspace, "runtime-sidecar.opened")
	if err := os.WriteFile(sidecarPath, []byte("sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecarHandle, sidecarInfo := open(t, sidecarPath)
	if protected, queryErr := prepared.ProtectsOpenedPath(
		sidecarPath,
		sidecarPath,
		sidecarHandle,
		sidecarInfo,
	); queryErr != nil || !protected {
		t.Fatalf("sidecar ProtectsOpenedPath() = %t, %v", protected, queryErr)
	}

	var nilPolicy *PreparedFileMutationPolicy
	if _, queryErr := nilPolicy.ProtectsPath(ordinaryFile); queryErr == nil {
		t.Fatal("nil ProtectsPath() accepted a query")
	}
	if _, queryErr := prepared.ProtectsPath("relative"); queryErr == nil {
		t.Fatal("relative ProtectsPath() accepted a query")
	}
	if _, queryErr := nilPolicy.OverlapsPath(ordinaryFile); queryErr == nil {
		t.Fatal("nil OverlapsPath() accepted a query")
	}
	if _, queryErr := prepared.OverlapsPath("relative"); queryErr == nil {
		t.Fatal("relative OverlapsPath() accepted a query")
	}
	if _, queryErr := nilPolicy.ProtectsOpenedFile(ordinaryHandle, ordinaryInfo); queryErr == nil {
		t.Fatal("nil ProtectsOpenedFile() accepted a query")
	}
	if _, queryErr := prepared.ProtectsOpenedFile(nil, ordinaryInfo); queryErr == nil {
		t.Fatal("nil opened file accepted")
	}
	if _, queryErr := prepared.ProtectsOpenedPath(
		ordinaryFile,
		"",
		ordinaryHandle,
		ordinaryInfo,
	); queryErr == nil {
		t.Fatal("incomplete opened path accepted")
	}
	if _, queryErr := prepared.ProtectsOpenedPath(
		"relative",
		"relative",
		ordinaryHandle,
		ordinaryInfo,
	); queryErr == nil {
		t.Fatal("relative opened path accepted")
	}
	if _, queryErr := prepared.ProtectsOpenedFile(ordinaryHandle, rootInfo); queryErr == nil {
		t.Fatal("mismatched opened-file preflight accepted")
	}

}

func TestPreparedFileMutationPolicyRootDriftFailsEveryQuery(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, "runtime")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	prepared, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedRoots: []string{root},
	})
	if err != nil {
		t.Fatal(err)
	}
	ordinary := filepath.Join(workspace, "ordinary.txt")
	if writeErr := os.WriteFile(ordinary, []byte("ordinary"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	ordinaryInfo, err := os.Stat(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryHandle, err := os.Open(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ordinaryHandle.Close() })
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, queryErr := prepared.ProtectsPath(ordinary); queryErr == nil ||
		!strings.Contains(queryErr.Error(), "root changed") {
		t.Fatalf("ProtectsPath() drift error = %v", queryErr)
	}
	if _, queryErr := prepared.OverlapsPath(ordinary); queryErr == nil ||
		!strings.Contains(queryErr.Error(), "root changed") {
		t.Fatalf("OverlapsPath() drift error = %v", queryErr)
	}
	if _, queryErr := prepared.ProtectsOpenedFile(ordinaryHandle, ordinaryInfo); queryErr == nil ||
		!strings.Contains(queryErr.Error(), "root changed") {
		t.Fatalf("ProtectsOpenedFile() drift error = %v", queryErr)
	}
}

func TestPreparedFileMutationPolicyConstructionFailureAndDeduplication(t *testing.T) {
	workspace := t.TempDir()
	if prepared, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedRoots: []string{"invalid\x00root"},
	}); err == nil || prepared != nil {
		t.Fatalf("invalid-root prepared policy = %#v, %v", prepared, err)
	}
	invalidPrefix := FileMutationPolicy{ProtectedSiblingPrefixes: []FileMutationSiblingPrefix{{
		Parent: workspace,
		Prefix: "../invalid",
	}}}
	if filesystem, err := buildMutationFS(workspace, true, nil, invalidPrefix); err == nil || filesystem != nil {
		t.Fatalf("invalid-prefix mutation filesystem = %#v, %v", filesystem, err)
	}
	prefix := FileMutationSiblingPrefix{Parent: workspace, Prefix: "sidecar."}
	prepared, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedSiblingPrefixes: []FileMutationSiblingPrefix{prefix, prefix},
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicateCandidate := filepath.Join(workspace, "sidecar.record")
	if protected, queryErr := prepared.ProtectsPath(duplicateCandidate); queryErr != nil || !protected {
		t.Fatalf("duplicate-prefix ProtectsPath() = %t, %v", protected, queryErr)
	}

	parent := filepath.Join(workspace, "prefix-parent")
	if mkdirErr := os.Mkdir(parent, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	drifted, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedSiblingPrefixes: []FileMutationSiblingPrefix{{
			Parent: parent,
			Prefix: "sidecar.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	moved := parent + "-moved"
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, parent); err == nil {
		if _, queryErr := drifted.OverlapsPath(workspace); queryErr == nil ||
			!strings.Contains(queryErr.Error(), "sibling parent changed") {
			t.Fatalf("sibling drift OverlapsPath() error = %v", queryErr)
		}
	}
}

func TestPolicyAwareReadAndListConstructors(t *testing.T) {
	workspace := t.TempDir()
	protectedDir := filepath.Join(workspace, "runtime")
	ordinaryDir := filepath.Join(workspace, "ordinary")
	for _, directory := range []string{protectedDir, ordinaryDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	protectedFile := filepath.Join(protectedDir, "secret.txt")
	ordinaryFile := filepath.Join(ordinaryDir, "visible.txt")
	if err := os.WriteFile(protectedFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ordinaryFile, []byte("visible\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := NewPreparedFileMutationPolicy(workspace, FileMutationPolicy{
		ProtectedRoots: []string{protectedDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	patterns := []*regexp.Regexp{regexp.MustCompile("^" + regexp.QuoteMeta(workspace))}
	reader, err := NewReadFileLinesToolWithPolicy(
		workspace,
		true,
		0,
		FileMutationPolicy{Prepared: prepared},
		patterns,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reader.maxSize != MaxReadFileSize || reader.Name() != "read_file" ||
		reader.Description() == "" || reader.Parameters()["type"] != "object" {
		t.Fatalf("policy reader metadata/max size = %d, %q", reader.maxSize, reader.Description())
	}
	if result := reader.Execute(context.Background(), map[string]any{
		"path": protectedFile,
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "protected runtime state") {
		t.Fatalf("protected read result = %#v", result)
	}
	if result := reader.Execute(context.Background(), map[string]any{
		"path": ordinaryFile,
	}); result == nil || result.IsError || !strings.Contains(result.ForLLM, "visible") {
		t.Fatalf("ordinary read result = %#v", result)
	}

	lister, err := NewListDirToolWithPolicy(
		workspace,
		true,
		FileMutationPolicy{Prepared: prepared},
		patterns,
	)
	if err != nil {
		t.Fatal(err)
	}
	if lister.Name() != "list_dir" || lister.Description() == "" ||
		lister.Parameters()["type"] != "object" {
		t.Fatalf("policy lister metadata = %q", lister.Description())
	}
	if result := lister.Execute(context.Background(), map[string]any{
		"path": protectedDir,
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "protected runtime state") {
		t.Fatalf("protected listing result = %#v", result)
	}
	if result := lister.Execute(context.Background(), map[string]any{
		"path": ordinaryDir,
	}); result == nil || result.IsError || !strings.Contains(result.ForLLM, "visible.txt") {
		t.Fatalf("ordinary listing result = %#v", result)
	}

	mixed := FileMutationPolicy{Prepared: prepared, ProtectedRoots: []string{protectedDir}}
	if tool, buildErr := NewReadFileLinesToolWithPolicy(
		workspace, true, 16, mixed,
	); buildErr == nil || tool != nil {
		t.Fatalf("mixed policy reader = %#v, %v", tool, buildErr)
	}
	if tool, buildErr := NewListDirToolWithPolicy(
		workspace, true, mixed,
	); buildErr == nil || tool != nil {
		t.Fatalf("mixed policy lister = %#v, %v", tool, buildErr)
	}

}
